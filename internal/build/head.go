package build

import (
	"cmp"
	"html"
	"html/template"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// touchIconSize is the side of the icon for Apple devices (apple-touch-icon), one of imaging.IconSizes.
const touchIconSize = 180

// alternate is a page for hreflang: a counterpart of a page in a language, or the page for the other languages.
type alternate struct {
	hreflang string // a language code, or "x-default"
	url      string // absolute and percent-encoded
}

// headImage is an image in the head of a page, such as its image for Open Graph.
type headImage struct {
	url           string // of its published file with its version, absolute and percent-encoded
	width, height int    // of its published file
	alt           string // in the language of the page; "" if none
}

// head returns what the core writes in <head> of p (Page.Head), one element on each line, with absolute URLs, in this
// order:
//   - the description of p (description), if it has one;
//   - its canonical URL (canonical), but not on a 404 page;
//   - robots noindex, if p asks for it (noindex);
//   - its alternates for hreflang (alternates);
//   - the RSS feed of its language;
//   - the icons of the site (iconLinks);
//   - Open Graph and the Twitter card, then the structured data (structuredData), but not on a 404 page.
func (b *builder) head(p *page) template.HTML {
	lang := p.language
	siteName := b.site.Name[lang]
	description := b.description(p)
	canonical := b.canonical(p)
	var lines []string
	if description != "" {
		lines = append(lines, tag("meta", "name", "description", "content", description))
	}
	if canonical != "" {
		lines = append(lines, tag("link", "rel", "canonical", "href", canonical))
	}
	if b.noindex(p) {
		lines = append(lines, tag("meta", "name", "robots", "content", "noindex"))
	}
	for _, a := range b.alternates(p) {
		lines = append(lines, tag("link", "rel", "alternate", "hreflang", a.hreflang, "href", a.url))
	}
	feed := b.absURL(b.site.LanguagePath(lang) + "feed/")
	lines = append(lines, tag("link", "rel", "alternate", "type", "application/rss+xml", "title", siteName,
		"href", feed))
	lines = append(lines, b.iconLinks()...)
	if p.kind == theme.KindNotFound {
		return template.HTML(strings.Join(lines, "\n"))
	}

	og := func(property, value string) string { return tag("meta", "property", property, "content", value) }
	ogType, title := "website", b.titleValue(p)
	if p.kind == theme.KindEntry && p.entry.e.Type == content.TypePost {
		ogType = "article"
	}
	if p.front {
		title = siteName
	}
	lines = append(lines, og("og:type", ogType), og("og:title", title))
	if description != "" {
		lines = append(lines, og("og:description", description))
	}
	lines = append(lines, og("og:url", canonical), og("og:site_name", siteName))
	image := b.ogImage(p)
	card := "summary"
	if image != nil {
		lines = append(lines,
			og("og:image", image.url),
			og("og:image:width", strconv.Itoa(image.width)),
			og("og:image:height", strconv.Itoa(image.height)),
		)
		if image.alt != "" {
			lines = append(lines, og("og:image:alt", image.alt))
		}
		card = "summary_large_image"
	}
	lines = append(lines, tag("meta", "name", "twitter:card", "content", card))
	if data := b.structuredData(p, canonical, description, image); data != nil {
		lines = append(lines, jsonLD(data))
	}
	return template.HTML(strings.Join(lines, "\n"))
}

// tag returns the start tag of an element named name, such as <link rel="canonical" href="…">, with the attributes in
// attrs, pairs of a name and a value, whose values it escapes.
func tag(name string, attrs ...string) string {
	var s strings.Builder
	s.WriteString("<" + name)
	for i := 0; i < len(attrs); i += 2 {
		s.WriteString(" " + attrs[i] + `="` + html.EscapeString(attrs[i+1]) + `"`)
	}
	s.WriteString(">")
	return s.String()
}

// absURL returns the URL of path, a path of the site such as "/about/", absolute and percent-encoded.
func (b *builder) absURL(path string) string {
	return render.EncodeURL(b.site.URL + path)
}

// canonical returns the canonical URL of p, absolute and percent-encoded: on the page of an entry and on the first page
// of a posts page, the seo.canonical of the entry, a path of which is from the site URL; otherwise the URL of p, also
// on the other pages of a list. It returns "" for a 404 page, which has none.
func (b *builder) canonical(p *page) string {
	switch {
	case p.kind == theme.KindNotFound:
		return ""
	case p.entry != nil && p.number == 1 && p.entry.rev.SEO.Canonical != "":
		c := p.entry.rev.SEO.Canonical
		if strings.HasPrefix(c, "/") {
			return b.absURL(c)
		}
		return render.EncodeURL(c)
	}
	return b.absURL(p.path)
}

// noindex reports whether p asks search engines not to index it: every page of a site with seo.noindex, the pages of an
// entry with seo.noindex (every page of a posts page), and the 404 pages.
func (b *builder) noindex(p *page) bool {
	return b.site.SEO.NoIndex || p.kind == theme.KindNotFound || p.entry != nil && p.entry.rev.SEO.NoIndex
}

// alternates returns the alternates of p for hreflang: on the first page of a page that has a counterpart in another
// language (counterparts), each of its counterparts that is not missing, p itself included, in the order of the
// languages of the site, and then x-default for that in the default language if it is not missing. It returns nil for
// other pages and for the 404 pages.
func (b *builder) alternates(p *page) []alternate {
	if p.number != 1 || p.kind == theme.KindNotFound {
		return nil
	}
	var as []alternate
	others := false
	xDefault := ""
	for _, c := range b.counterparts(p) {
		if c.missing {
			continue
		}
		url := b.absURL(c.path)
		as = append(as, alternate{hreflang: c.language, url: url})
		others = others || c.language != p.language
		if c.language == b.site.DefaultLanguage {
			xDefault = url
		}
	}
	if !others {
		return nil
	}
	if xDefault != "" {
		as = append(as, alternate{hreflang: "x-default", url: xDefault})
	}
	return as
}

// iconLinks returns the links to the icons of the site, if it has an icon whose original can be inspected: the icons
// of 32, 192 and 512 pixels with the sizes of their files, each file once, and then that for Apple devices.
func (b *builder) iconLinks() []string {
	if b.site.Icon == "" {
		return nil
	}
	var links []string
	touch := ""
	for _, ic := range b.res.icons(b.site.Icon) {
		url := b.absURL(ic.path)
		if ic.size == touchIconSize {
			touch = tag("link", "rel", "apple-touch-icon", "href", url)
			continue
		}
		sizes := strconv.Itoa(ic.width) + "x" + strconv.Itoa(ic.height)
		link := tag("link", "rel", "icon", "href", url, "sizes", sizes, "type", "image/png")
		if !slices.Contains(links, link) {
			links = append(links, link)
		}
	}
	if touch != "" {
		links = append(links, touch)
	}
	return links
}

// ogImage returns the image for Open Graph of p: the seo.ogImage of its entry, else its featuredImage, else the
// seo.ogImage of the site in the language of p; nil if there is none or its original cannot be inspected.
func (b *builder) ogImage(p *page) *headImage {
	id := ""
	if p.entry != nil {
		id = cmp.Or(p.entry.rev.SEO.OGImage, p.entry.rev.FeaturedImage)
	}
	if id = cmp.Or(id, b.site.SEO.OGImage[p.language]); id == "" {
		return nil
	}
	return b.headImage(id, p.language)
}

// headImage returns the image medium with the ID as a page in language shows it in its head: its published file, which
// it records as asked about. It returns nil if there is no such image or its original cannot be inspected, which
// publishMedia reports.
func (b *builder) headImage(id, language string) *headImage {
	d := b.media[id]
	if d == nil || d.v.Kind() != content.MediaImage {
		return nil
	}
	f, err := b.res.published(id, d)
	if err != nil {
		return nil
	}
	return &headImage{url: b.absURL(f.versioned()), width: f.width, height: f.height, alt: d.v.Alt[language]}
}
