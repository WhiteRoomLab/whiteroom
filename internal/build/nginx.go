package build

import (
	"bytes"
	"strconv"
	"strings"
)

// nginxEscaper escapes `"` and "\" in a string in double quotes of the configuration of nginx, where "\" also starts
// escapes such as "\n".
var nginxEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// nginxConf returns nginx.conf, a part of the configuration of nginx to include in a server block whose root serves
// the public directory at the path of the site URL; it does not set the root. In order, it has:
//   - the directory index;
//   - the 404 pages, with error_page for the whole server block: the page of the paths outside the other languages,
//     or a variable that is set to the page of the language whose path the request has;
//   - an exact location that answers .htaccess, the file of Apache in the public directory, with 404;
//   - for each path of the redirects, as nginx decodes paths, exact locations with and without the trailing "/" (one
//     for the root of the site), each with the redirects with queries in the order of precedence and, if any, the
//     redirect without a query at the end.
//
// A query matches with a lookahead for each parameter of the from, so that the request may have them in any order and
// have others. c nil leaves only the directory index.
func (b *builder) nginxConf(c *serverConfig) []byte {
	var w bytes.Buffer
	w.WriteString("index " + strings.Join(directoryIndex, " ") + ";\n")
	if c == nil {
		return w.Bytes()
	}
	if n := len(c.notFound); n > 0 {
		page := c.notFound[n-1].Page // that of "/", the shortest prefix
		var others []notFoundPage
		for _, p := range c.notFound[:n-1] {
			if p.Page != page {
				others = append(others, p)
			}
		}
		if len(others) == 0 {
			w.WriteString("error_page 404 " + nginxString(c.base+page) + ";\n")
		} else {
			w.WriteString("set $whiteroom_not_found " + nginxString(c.base+page) + ";\n")
			for _, p := range others {
				w.WriteString("if ($uri ~ " + nginxRegexp("^"+quoteRegexp(c.base+p.Prefix)) + ") {\n")
				w.WriteString("    set $whiteroom_not_found " + nginxString(c.base+p.Page) + ";\n")
				w.WriteString("}\n")
			}
			w.WriteString("error_page 404 $whiteroom_not_found;\n")
		}
	}
	w.WriteString("location = " + nginxString(c.base+"/.htaccess") + " {\n    return 404;\n}\n")
	for _, g := range c.groups {
		paths := []string{c.base + g.path}
		if g.path != "/" {
			paths = append(paths, c.base+g.path+"/")
		}
		for _, path := range paths {
			w.WriteString("location = " + nginxString(path) + " {\n")
			for _, r := range g.rules {
				// nginx reads "$" in the URL as a variable, and has no escape for it.
				ret := "return " + strconv.Itoa(r.status) + " " + nginxString(strings.ReplaceAll(r.location, "$", "%24"))
				if len(r.params) == 0 {
					w.WriteString("    " + ret + ";\n")
				} else {
					w.WriteString("    if ($args ~ " + nginxRegexp(argsRegexp(r.params)) + ") {\n")
					w.WriteString("        " + ret + ";\n")
					w.WriteString("    }\n")
				}
			}
			w.WriteString("}\n")
		}
	}
	return w.Bytes()
}

// nginxString returns s as a string in double quotes for the configuration of nginx. The characters other than "\"
// and `"` stay as they are, also the bytes other than ASCII.
func nginxString(s string) string {
	return `"` + nginxEscaper.Replace(s) + `"`
}

// nginxRegexp returns re in double quotes for the configuration of nginx, which passes "\x" escapes to PCRE as they
// are. re must have no `"` and "\" only before "x", as quoteRegexp makes them.
func nginxRegexp(re string) string {
	return `"` + re + `"`
}

// argsRegexp returns a regular expression that the query of a request, as it is sent, matches if it has each of params,
// query parameters in the form that browsers send, in any order and among others: a lookahead for each.
func argsRegexp(params []string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, p := range params {
		b.WriteString("(?=(?:.*&)?" + quoteRegexp(p) + "(?:&|$))")
	}
	return b.String()
}
