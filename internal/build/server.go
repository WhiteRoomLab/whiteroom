package build

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// directoryIndex are the files that the servers serve for a path that ends with "/", in this order: those of the pages,
// and those of the feeds.
var directoryIndex = []string{"index.html", "index.xml"}

// serverList is server.json, what the built-in server needs to serve the public directory of a release.
type serverList struct {
	Base      string           `json:"base"` // the path of the site URL in the canonical form of froms; "" at the root
	Redirects []serverRedirect `json:"redirects"`
	NotFound  []notFoundPage   `json:"notFound"`
	Index     []string         `json:"index"`
}

// serverRedirect is a redirect in server.json.
type serverRedirect struct {
	From   string `json:"from"`
	To     string `json:"to"` // the Location of the responses
	Status int    `json:"status"`
}

// notFoundPage is the 404 page of the paths that start with Prefix, a path from the site URL that ends with "/".
type notFoundPage struct {
	Prefix string `json:"prefix"`
	Page   string `json:"page"` // its path from the site URL
}

// serverConfig is what nginx.conf and .htaccess have apart from the directory index.
type serverConfig struct {
	base     string // the path of the site URL as nginx and Apache decode it, such as "/blog"; "" at the root
	notFound []notFoundPage
	groups   []redirectGroup
}

// redirectGroup is the redirects whose froms have the same path as nginx and Apache decode it, in the order of their
// precedence (6.6.6).
type redirectGroup struct {
	path  string // decoded, without the trailing "/" unless it is "/"
	rules []redirectRule
}

// redirectRule is a redirect as nginx and Apache match it.
type redirectRule struct {
	params   []string // the query parameters of the from, in the form that browsers send (browserParam)
	location string   // the Location of the responses, as in server.json
	status   int
}

// writeServerFiles writes into rel, the directory of a release, the files for the servers of its public directory:
// server.json for the built-in server, nginx.conf for nginx and public/.htaccess for Apache, with the redirects of
// b.serverRedirects, the 404 pages of the languages and the directory index. A redirect that nginx and Apache cannot
// match as the built-in server does is left out of nginx.conf and .htaccess, with a warning; so is everything but the
// directory index if they cannot have the path of the site URL. The error is one of writing.
func (b *builder) writeServerFiles(rel *os.Root) error {
	pages := b.notFoundPages()
	list := serverList{Base: content.CanonicalFrom(b.base), NotFound: pages, Index: directoryIndex}
	for _, r := range b.serverRedirects {
		list.Redirects = append(list.Redirects, serverRedirect{From: r.from, To: b.location(r), Status: r.status})
	}
	data, err := json.Marshal(list, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	c := b.serverConfig(pages)
	if err := writeFile(rel, "server.json", append(data, '\n')); err != nil {
		return err
	}
	if err := writeFile(rel, "nginx.conf", b.nginxConf(c)); err != nil {
		return err
	}
	return writeFile(rel, "public/.htaccess", b.htaccess(c))
}

// location returns the Location of the responses of r: the URL of its target, percent-encoded, with the path of the
// site URL before a path.
func (b *builder) location(r redirect) string {
	if r.abs {
		return render.EncodeURL(r.to)
	}
	return render.EncodeURL(b.base + r.to)
}

// notFoundPages returns the 404 pages of the site by the paths of their languages, the longest first; with
// prefixDefaultLanguage, that of the default language is also the page of the other paths ("/").
func (b *builder) notFoundPages() []notFoundPage {
	var pages []notFoundPage
	for _, p := range b.pages {
		if p.kind == theme.KindNotFound {
			pages = append(pages, notFoundPage{Prefix: b.site.LanguagePath(p.language), Page: p.path})
		}
	}
	if b.site.PrefixDefaultLanguage {
		prefix := b.site.LanguagePath(b.site.DefaultLanguage)
		if i := slices.IndexFunc(pages, func(x notFoundPage) bool { return x.Prefix == prefix }); i >= 0 {
			pages = append(pages, notFoundPage{Prefix: "/", Page: pages[i].Page})
		}
	}
	slices.SortStableFunc(pages, func(x, y notFoundPage) int { return cmp.Compare(len(y.Prefix), len(x.Prefix)) })
	return pages
}

// serverConfig returns what nginx.conf and .htaccess have apart from the directory index: the 404 pages, and the
// redirects that nginx and Apache can match (redirectGroups). It returns nil, with a warning, if the path of the site
// URL cannot be written for them.
func (b *builder) serverConfig(pages []notFoundPage) *serverConfig {
	base, reason := decodePath(b.base)
	switch {
	case reason != "":
	case strings.Contains(base, "$"):
		reason = `"$", which nginx reads as a variable`
	case strings.Contains(base, "?"):
		reason = `"?" (%3F), which nginx reads as the start of a query`
	}
	if reason != "" {
		b.warnings.add(b.settings, "/url", "the path of the site URL has %s; nginx.conf and .htaccess have only the "+
			"directory index, so only the built-in server serves the redirects and the 404 pages", reason)
		return nil
	}
	return &serverConfig{base: base, notFound: pages, groups: b.redirectGroups()}
}

// redirectGroups returns the redirects of b.serverRedirects grouped by the paths of their froms as nginx and Apache
// decode them, in the order of the paths, each group in the order of precedence. A redirect that they cannot match as
// the built-in server does is left out with a warning: one whose path decodePath does not take, or that of .htaccess;
// and one that has the decoded path and the query of a redirect before it in the order of precedence, such as "/a+b"
// after "/a%2Bb".
func (b *builder) redirectGroups() []redirectGroup {
	rs := slices.Clone(b.serverRedirects)
	slices.SortStableFunc(rs, func(x, y redirect) int { return comparePrecedence(x.from, y.from) })
	var groups []redirectGroup
	index := make(map[string]int) // of the groups by path
	firsts := make(map[string]redirect)
	for _, r := range rs {
		path, query, _ := strings.Cut(r.from, "?")
		path, reason := decodePath(path)
		if reason != "" {
			reason = "has " + reason
		} else if path = trimSlash(path); path == "/.htaccess" {
			reason = "is that of .htaccess"
		}
		if reason != "" {
			b.warnings.add(r.src.file, r.src.pointer, "the path %s; the redirect is left out of nginx.conf and "+
				".htaccess, so only the built-in server serves it", reason)
			continue
		}
		if first, taken := claim(firsts, path+"?"+query, r); taken {
			b.warnings.add(r.src.file, r.src.pointer, "nginx and Apache decode the path as that of the from %q of %s; "+
				"the redirect is left out of nginx.conf and .htaccess, so only the built-in server serves it",
				first.from, first.src.what)
			continue
		}
		var params []string
		if query != "" {
			for p := range strings.SplitSeq(query, "&") {
				params = append(params, browserParam(p))
			}
		}
		i, ok := index[path]
		if !ok {
			i = len(groups)
			index[path] = i
			groups = append(groups, redirectGroup{path: path})
		}
		groups[i].rules = append(groups[i].rules, redirectRule{params: params, location: b.location(r), status: r.status})
	}
	slices.SortFunc(groups, func(x, y redirectGroup) int { return strings.Compare(x.path, y.path) })
	return groups
}

// decodePath returns path, percent-encoded or not, as nginx and Apache decode the paths of requests before they match
// them, or what it has that they cannot match as the built-in server does: an encoded "/", which nginx decodes and
// Apache rejects; bytes that are not UTF-8 or control characters, which nginx.conf would have as they are; or an empty
// segment, which both merge with the next.
func decodePath(path string) (decoded, reason string) {
	if strings.Contains(path, "%2F") || strings.Contains(path, "%2f") {
		return "", `an encoded "/" (%2F)`
	}
	decoded, err := url.PathUnescape(path)
	switch {
	case err != nil:
		return "", "an invalid percent-encoding"
	case !utf8.ValidString(decoded):
		return "", "bytes that are not UTF-8"
	case strings.ContainsFunc(decoded, unicode.IsControl):
		return "", "a control character"
	case strings.Contains(decoded, "//"):
		return "", "an empty segment"
	}
	return decoded, ""
}

// browserParam returns p, a query parameter in the canonical form of froms, as browsers send it: with the bytes of the
// characters other than ASCII percent-encoded in upper case. The canonical form already encodes the other characters
// that browsers encode, but also some that they send as they are, such as "[" and "|".
func browserParam(p string) string {
	var b strings.Builder
	for i := range len(p) {
		if c := p[i]; c >= utf8.RuneSelf {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// quoteRegexp returns a regular expression of PCRE that matches s, for nginx and Apache: the bytes other than ASCII
// letters, digits, "-", "_", "/", "=" and "%" as "\x" and two hex digits, which their configuration files pass to PCRE
// as they are, and which match the bytes, as both use PCRE without UTF mode.
func quoteRegexp(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_/=%", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, `\x%02X`, c)
		}
	}
	return b.String()
}
