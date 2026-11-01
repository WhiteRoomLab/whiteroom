package build

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// The namespaces of the sitemaps: that of sitemaps.org, and XHTML for the alternates (xhtml:link).
const (
	sitemapNS = "http://www.sitemaps.org/schemas/sitemap/0.9"
	xhtmlNS   = "http://www.w3.org/1999/xhtml"
)

// The most URLs and bytes of a sitemap file (sitemaps.org); a larger sitemap is split into files that a sitemap index
// lists. Variables, so that tests can lower them.
var (
	maxSitemapURLs  = 50000
	maxSitemapBytes = 50 << 20
)

// maxSitemapLoc is the most characters of a URL in a sitemap, which sitemaps.org takes if it has less than 2,048.
const maxSitemapLoc = 2047

// urlsetEnd ends a sitemap file that is not a sitemap index.
const urlsetEnd = "</urlset>\n"

// sitemapPage is a page that the sitemap lists.
type sitemapPage struct {
	loc string // its URL, absolute and percent-encoded
	p   *page
}

// writeSitemap writes the sitemap of the site into pub as sitemap.xml, unless the site has seo.noindex: the pages of
// sitemapPages, each with its alternates for hreflang, as its head has them (alternates). A sitemap of more than
// maxSitemapURLs URLs or maxSitemapBytes bytes is split, in that order, into sitemap-1.xml, sitemap-2.xml and so on,
// which writeSitemap adds to b.outputs, and sitemap.xml is then the sitemap index of their URLs. The error is one of
// writing.
func (b *builder) writeSitemap(pub *os.Root) error {
	if b.site.SEO.NoIndex {
		return nil
	}
	var w xmlWriter // the file being written
	begin := func() {
		w.Reset()
		w.WriteString(xmlHeader)
		w.start("urlset", "xmlns", sitemapNS, "xmlns:xhtml", xhtmlNS)
	}
	begin()
	parts, n := 0, 0 // the files written, and the URLs in w
	var u xmlWriter
	for _, x := range b.sitemapPages() {
		u.Reset()
		u.start("url")
		u.text("loc", x.loc)
		for _, a := range b.alternates(x.p) {
			u.empty("xhtml:link", "rel", "alternate", "hreflang", a.hreflang, "href", a.url)
		}
		u.end("url")
		if n > 0 && (n == maxSitemapURLs || w.Len()+u.Len()+len(urlsetEnd) > maxSitemapBytes) {
			w.WriteString(urlsetEnd)
			parts++
			if err := b.writeSitemapPart(pub, parts, w.Bytes()); err != nil {
				return err
			}
			begin()
			n = 0
		}
		w.Write(u.Bytes())
		n++
	}
	w.WriteString(urlsetEnd)
	if parts == 0 {
		return writeFile(pub, "sitemap.xml", w.Bytes())
	}
	parts++
	if err := b.writeSitemapPart(pub, parts, w.Bytes()); err != nil {
		return err
	}
	w.Reset()
	w.WriteString(xmlHeader)
	w.start("sitemapindex", "xmlns", sitemapNS)
	for i := 1; i <= parts; i++ {
		w.start("sitemap")
		w.text("loc", b.absURL("/"+sitemapPart(i)))
		w.end("sitemap")
	}
	w.end("sitemapindex")
	return writeFile(pub, "sitemap.xml", w.Bytes())
}

// sitemapPart returns the file of the part n of a split sitemap, from 1: sitemap-<n>.xml.
func sitemapPart(n int) string {
	return "sitemap-" + strconv.Itoa(n) + ".xml"
}

// writeSitemapPart writes data as the part n of a split sitemap into pub, and adds it to b.outputs.
func (b *builder) writeSitemapPart(pub *os.Root, n int, data []byte) error {
	file := sitemapPart(n)
	if err := writeFile(pub, file, data); err != nil {
		return err
	}
	b.addOutput("/"+file, file, source{what: "the sitemap"}, rankCore)
	return nil
}

// sitemapPages returns the pages that the sitemap lists (inSitemap), sorted by their URLs. A page whose URL has more
// than maxSitemapLoc characters is left out with a warning at where the page comes from.
func (b *builder) sitemapPages() []sitemapPage {
	var srcs map[string]source // where the pages come from, by path, made for the first warning
	var xs []sitemapPage
	for _, p := range b.pages {
		if !b.inSitemap(p) {
			continue
		}
		loc := b.absURL(p.path)
		if len(loc) > maxSitemapLoc {
			if srcs == nil {
				srcs = make(map[string]source, len(b.outputs))
				for _, o := range b.outputs {
					srcs[o.path] = o.src
				}
			}
			src := srcs[p.path]
			b.warnings.add(src.file, src.pointer,
				"the URL of the page %s has %d characters, more than a sitemap takes (%d); the page is left out of it",
				p.path, len(loc), maxSitemapLoc)
			continue
		}
		xs = append(xs, sitemapPage{loc: loc, p: p})
	}
	slices.SortStableFunc(xs, func(x, y sitemapPage) int { return strings.Compare(x.loc, y.loc) })
	return xs
}

// inSitemap reports whether the sitemap lists p: the first page of an entry, or of a list with entries, unless search
// engines should not index it (noindex), as the 404 pages, or its canonical URL is another (canonical).
func (b *builder) inSitemap(p *page) bool {
	switch {
	case p.number != 1, b.noindex(p):
		return false
	case p.kind != theme.KindEntry && len(p.entries) == 0:
		return false
	}
	return b.canonical(p) == b.absURL(p.path)
}
