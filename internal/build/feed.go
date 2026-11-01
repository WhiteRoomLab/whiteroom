package build

import (
	"cmp"
	"context"
	"os"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// The namespaces of the RSS feeds: the content module (content:encoded), Dublin Core (dc:creator) and Atom
// (atom:link).
const (
	contentNS = "http://purl.org/rss/1.0/modules/content/"
	dcNS      = "http://purl.org/dc/elements/1.1/"
	atomNS    = "http://www.w3.org/2005/Atom"
)

// htmlEscaper escapes plain text as text of HTML, for the description of an item, which RSS takes as HTML.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// feedPath returns the path of the RSS feed in language: feed/ after the language path, such as "/en/feed/".
func (b *builder) feedPath(language string) string {
	return b.site.LanguagePath(language) + "feed/"
}

// writeFeeds writes the RSS 2.0 feed of each language of the site (feed) into pub, as index.xml at its path
// (feedPath), in the order of the settings. A feed has the latest posts of its language, as many as a page of a list
// has, newest first as lists have them but without the sticky posts first; a language without posts gets a channel
// without items. It renders the bodies of the posts again (renderEntry), with the URL of the site as the base, so that
// their URLs are absolute; their warnings, the same as those of their pages, go to b.warnings. b.res must be set. The
// error is ctx.Err() if ctx is done, that of rendering a body, or one of writing.
func (b *builder) writeFeeds(ctx context.Context, pub *os.Root) error {
	posts := b.members()
	for _, lang := range b.site.Languages {
		if err := ctx.Err(); err != nil {
			return err
		}
		es := posts[listKey{theme.KindPosts, lang, ""}]
		sortEntries(es, false)
		data, err := b.feed(ctx, lang, es[:min(len(es), b.postsPerPage())])
		if err != nil {
			return err
		}
		if err := writeFile(pub, strings.TrimPrefix(b.feedPath(lang), "/")+"index.xml", data); err != nil {
			return err
		}
	}
	return nil
}

// feed returns the RSS feed in language with the items of es, published posts. Its channel has the site name in the
// language, the URL of the front page of the language, the tagline, else the site name, the language code, and the URL
// of the feed (atom:link). Each item has the title of its post, the URL of its page, its ID as a guid that is not a
// permalink, its date in the time zone of the site (RFC 1123 with a numeric zone), the name of its author
// (dc:creator), its terms (category) by taxonomy in the order of its type, its excerpt (excerpt) on one line as HTML
// (description), and its body (content:encoded); each only if it has one.
func (b *builder) feed(ctx context.Context, language string, es []*entryDoc) ([]byte, error) {
	tr, err := b.theme.Renderer(language, b.site.URL)
	if err != nil {
		return nil, err
	}
	r := render.New(tr, b.res)
	name := b.site.Name[language]
	var w xmlWriter
	w.WriteString(xmlHeader)
	w.start("rss", "version", "2.0", "xmlns:content", contentNS, "xmlns:dc", dcNS, "xmlns:atom", atomNS)
	w.start("channel")
	w.plain("title", name)
	w.plain("link", b.absURL(b.site.LanguagePath(language)))
	w.plain("description", cmp.Or(b.site.Tagline[language], name))
	w.plain("language", language)
	w.empty("atom:link", "href", b.absURL(b.feedPath(language)), "rel", "self", "type", "application/rss+xml")
	for _, d := range es {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		html, _, err := b.renderEntry(d, r)
		if err != nil {
			return nil, err
		}
		rev := d.rev
		w.start("item")
		w.plain("title", rev.Title)
		w.plain("link", b.absURL(b.entryPaths[d.e.ID]))
		w.plain("guid", d.e.ID, "isPermaLink", "false")
		w.plain("pubDate", listTime(d).In(b.loc).Format(time.RFC1123Z))
		if rev.Author != "" {
			w.plain("dc:creator", b.authors[rev.Author].v.Name)
		}
		for _, taxonomy := range b.site.Type(d.e.Type).Taxonomies {
			for _, id := range rev.Terms[taxonomy] {
				w.plain("category", b.terms[id].v.Name)
			}
		}
		if s := oneLine(excerpt(rev)); s != "" {
			w.text("description", htmlEscaper.Replace(s))
		}
		if body := html["/body"]; body != "" {
			w.text("content:encoded", string(body))
		}
		w.end("item")
	}
	w.end("channel")
	w.end("rss")
	return w.Bytes(), nil
}
