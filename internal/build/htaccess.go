package build

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// substEscaper escapes "%" and "$" in a substitution of RewriteRule, where they start back-references and variables.
var substEscaper = strings.NewReplacer("%", `\%`, "$", `\$`)

// htaccess returns public/.htaccess, the configuration of Apache for the public directory, which needs AllowOverride
// FileInfo and Indexes, and mod_dir and, if there are redirects, mod_rewrite. It does not keep itself from being
// served, as Require would need AllowOverride AuthConfig: the configuration of the server does, as the default one
// does with <Files ".ht*">. In order, it has:
//   - the directory index;
//   - the 404 pages: the page of the paths outside the other languages, then that of each other language in an If
//     section by the path of the request;
//   - if there are redirects, the rewrite engine on, and for each redirect, by the paths of the froms and then in the
//     order of precedence, a RewriteCond for each parameter of its query and a RewriteRule that matches its path, as
//     Apache decodes paths, relative to the public directory and with an optional trailing "/", and redirects without
//     the query of the request.
//
// c nil leaves only the directory index.
func (b *builder) htaccess(c *serverConfig) []byte {
	var w bytes.Buffer
	w.WriteString("DirectoryIndex " + strings.Join(directoryIndex, " ") + "\n")
	if c == nil {
		return w.Bytes()
	}
	if n := len(c.notFound); n > 0 {
		// ErrorDocument would read "$" and a digit as a back-reference, but serverConfig keeps "$" out of b.base.
		page := c.notFound[n-1].Page // that of "/", the shortest prefix
		w.WriteString("ErrorDocument 404 " + render.EncodeURL(b.base+page) + "\n")
		for _, p := range c.notFound[:n-1] {
			if p.Page != page {
				w.WriteString(`<If "%{REQUEST_URI} =~ m#^` + quoteRegexp(c.base+p.Prefix) + `#">` + "\n")
				w.WriteString("    ErrorDocument 404 " + render.EncodeURL(b.base+p.Page) + "\n")
				w.WriteString("</If>\n")
			}
		}
	}
	if len(c.groups) == 0 {
		return w.Bytes()
	}
	w.WriteString("RewriteEngine On\n")
	for _, g := range c.groups {
		pattern := "^$"
		if g.path != "/" {
			pattern = "^" + quoteRegexp(g.path[1:]) + "/?$"
		}
		for _, r := range g.rules {
			for _, p := range r.params {
				w.WriteString("RewriteCond %{QUERY_STRING} (?:^|&)" + quoteRegexp(p) + "(?:&|$)\n")
			}
			w.WriteString("RewriteRule " + pattern + " " + substEscaper.Replace(r.location) +
				" [R=" + strconv.Itoa(r.status) + ",L,QSD,NE]\n")
		}
	}
	return w.Bytes()
}
