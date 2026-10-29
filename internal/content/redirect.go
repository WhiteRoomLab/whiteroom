package content

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Redirect forwards requests for an old URL in the site, such as one from WordPress, to a new one.
type Redirect struct {
	ID     string
	From   string // a path in the site with an optional query, such as "/?p=123", in the canonical form
	To     string // the ID of an entry, a term, an author or a medium, a path in the site, or an http or https URL
	Status int    // 301 or 302
}

func (x *Redirect) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Redirect) decode(d *decoder) error {
	var v Redirect
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "from":
			v.From, err = d.string()
		case "to":
			v.To, err = d.string()
		case "status":
			v.Status, err = readInt(d, checkRedirectStatus)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	if !version {
		return d.errorf(`missing key "schemaVersion"`)
	}
	*x = v
	return nil
}

func (x Redirect) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		ID            string `json:"id,omitempty"`
		From          string `json:"from,omitempty"`
		To            string `json:"to,omitempty"`
		Status        int    `json:"status,omitzero"`
	}{
		SchemaVersion: SchemaVersion,
		ID:            x.ID,
		From:          x.From,
		To:            x.To,
		Status:        x.Status,
	})
}

// Normalize puts From in the canonical form and cleans up To as Revision.Normalize does URLs.
func (x *Redirect) Normalize() {
	x.From = normalizeRedirectFrom(x.From)
	x.To = normalizeHref(x.To)
}

// Matches reports whether x applies to target, the path and the query of a request, as redirectMatches does.
// The caller chooses among the redirects that match the same request.
func (x Redirect) Matches(target string) bool { return redirectMatches(x.From, target) }

// Validate checks x. Every item is required.
// lookup tells about the entry, term, author or medium of To as ValidateOptions.Lookup does; nil skips checking that it exists.
func (x Redirect) Validate(lookup func(kind RefKind, id string) (Ref, bool)) error {
	c := checker{doc: &docState{opts: ValidateOptions{Lookup: lookup}}}
	x.check(&c)
	return c.result()
}

func (x Redirect) check(c *checker) {
	c.required("id", x.ID, func(s string) error { return checkID(RefRedirect, s) })
	c.required("from", x.From, checkRedirectFrom)
	x.checkTo(c)
	if x.Status == 0 {
		c.addf("/status", "missing status")
	} else {
		c.add("/status", checkRedirectStatus(x.Status))
	}
}

// redirectTargets are the kinds of entities that redirects can go to, in the order that the message of checkTo lists them.
var redirectTargets = []RefKind{RefEntry, RefTerm, RefAuthor, RefMedia}

func (x Redirect) checkTo(c *checker) {
	// Classified in the normalized form, so that " /about/" is reported as not normalized, not as an invalid target.
	t := normalizeHref(x.To)
	switch {
	case x.To == "":
		c.addf("/to", "missing to")
	case strings.HasPrefix(t, "/") || strings.Contains(t, ":"):
		c.add("/to", x.checkURL())
	default:
		for _, kind := range redirectTargets {
			if strings.HasPrefix(t, idPrefixes[kind]) {
				c.ref("/to", kind, x.To)
				return
			}
		}
		c.addf("/to", "invalid target %q: use the ID of an entry, a term, an author or a medium, "+
			`a path starting with "/", or an http or https URL`, x.To)
	}
}

// checkURL checks To, a path in the site or a URL, which must not lead back to From.
func (x Redirect) checkURL() error {
	if err := checkHref(x.To); err != nil {
		return err
	}
	switch u, _ := url.Parse(x.To); u.Scheme {
	case "http", "https":
	case "":
		if checkRedirectFrom(x.From) == nil && redirectMatches(x.From, x.To) {
			return errors.New("redirects to itself")
		}
	default:
		return errors.New(`must be a path starting with "/" or an http or https URL`)
	}
	return nil
}

func checkRedirectStatus(v int) error {
	if v != 301 && v != 302 {
		return fmt.Errorf("unsupported status %d (use 301 or 302)", v)
	}
	return nil
}

// normalizeRedirectFrom returns from, a path in the site with an optional query, percent-encoded only where needed
// (visible characters other than ASCII are decoded, as in IRIs) and with the query parameters sorted by name.
// It keeps the case and a trailing "/", and does not apply NFC, which would break old URLs of files named in NFD.
func normalizeRedirectFrom(s string) string {
	path, params := splitRedirectFrom(s)
	if len(params) == 0 {
		return path
	}
	return path + "?" + strings.Join(params, "&")
}

// splitRedirectFrom returns the path and the query parameters of from in the canonical form, sorted by compareParams.
func splitRedirectFrom(s string) (path string, params []string) {
	path, query, _ := strings.Cut(normalizeHref(s), "?")
	for p := range strings.SplitSeq(query, "&") {
		if p != "" {
			params = append(params, canonicalPart(p, true))
		}
	}
	slices.SortStableFunc(params, compareParams)
	return canonicalPart(path, false), params
}

// compareParams orders query parameters by name, the part before the first "=", and then as strings.
func compareParams(a, b string) int {
	return cmp.Or(cmp.Compare(paramName(a), paramName(b)), cmp.Compare(a, b))
}

func paramName(p string) string {
	name, _, _ := strings.Cut(p, "=")
	return name
}

// canonicalPart returns s, the path of from or a parameter of its query, in the canonical form.
func canonicalPart(s string, query bool) string {
	var b strings.Builder
	keep := func(c byte) bool { return keepUnescaped(c, query) }
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			writeChars(&b, s, keep)
			return b.String()
		}
		writeChars(&b, s[:i], keep)
		p, rest := unescape(s[i:])
		if p == "" {
			// A bad "%" is left for checkRedirectFrom, with the escapes after it: decoding "%%34%31" would make a new one, "%41".
			writeChars(&b, s[i:], func(c byte) bool { return c == '%' || keep(c) })
			return b.String()
		}
		writeChars(&b, p, isUnreserved)
		s = rest
	}
}

const upperHex = "0123456789ABCDEF"

// writeChars writes s to b, keeping the ASCII characters that keep accepts and the visible ones (graphic and not spaces)
// other than ASCII. The rest, such as format characters and invalid bytes, are percent-encoded in UTF-8.
func writeChars(b *strings.Builder, s string, keep func(byte) bool) {
	for s != "" {
		r, n := utf8.DecodeRuneInString(s)
		// n is 1 for an invalid byte, which is decoded as U+FFFD.
		if r < utf8.RuneSelf && keep(byte(r)) || n > 1 && unicode.IsGraphic(r) && !unicode.IsSpace(r) {
			b.WriteString(s[:n])
		} else {
			for i := range n {
				b.WriteByte('%')
				b.WriteByte(upperHex[s[i]>>4])
				b.WriteByte(upperHex[s[i]&15])
			}
		}
		s = s[n:]
	}
}

// keepUnescaped reports whether c, an ASCII character that is not percent-encoded, is kept as it is in a part of from:
// unreserved characters, sub-delims, ":", "@" and "/" as RFC 3986 allows, and "?" in the query.
func keepUnescaped(c byte, query bool) bool {
	switch c {
	case '!', '$', '&', '(', ')', '*', '+', ',', ';', '=', ':', '@', '/':
		return true
	case '\'':
		// Browsers send it as %27 in queries.
		return !query
	case '?':
		return query
	case '#', '\\':
		// Left for checkRedirectFrom to report.
		return true
	}
	return isUnreserved(c)
}

// isUnreserved reports whether c is an unreserved character of RFC 3986, which means the same percent-encoded or not.
func isUnreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

// unescape decodes the escapes at the start of s, such as "%E4%BC%9A" in "%E4%BC%9A/", and returns the rest of s.
func unescape(s string) (p, rest string) {
	var b []byte
	for {
		c, ok := unescapeByte(s)
		if !ok {
			return string(b), s
		}
		b = append(b, c)
		s = s[3:]
	}
}

// unescapeByte decodes the escape at the start of s, such as "%2F" in "%2Fa".
func unescapeByte(s string) (byte, bool) {
	if len(s) < 3 || s[0] != '%' {
		return 0, false
	}
	c, err := strconv.ParseUint(s[1:3], 16, 8)
	return byte(c), err == nil
}

// hasBadEscape reports whether s has a "%" without two hex digits after it.
func hasBadEscape(s string) bool {
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			return false
		}
		if _, ok := unescapeByte(s[i:]); !ok {
			return true
		}
		s = s[i+3:]
	}
}

// checkRedirectFrom accepts from in the canonical form, without what browsers never send, such as fragments.
func checkRedirectFrom(s string) error {
	if err := checkSize(s); err != nil {
		return err
	}
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	if strings.ContainsFunc(s, unicode.IsControl) || strings.TrimFunc(s, unicode.IsSpace) != s {
		return errors.New("contains control characters or surrounding spaces; normalize it first")
	}
	if !strings.HasPrefix(s, "/") {
		return errors.New(`must be a path starting with "/"`)
	}
	if strings.HasPrefix(s, "//") {
		return errors.New("protocol-relative URLs are not allowed")
	}
	// Browsers send "\" in paths as "/".
	if strings.Contains(s, `\`) {
		return errors.New("backslashes are not allowed")
	}
	if strings.Contains(s, "#") {
		return errors.New("fragments are not allowed; browsers never send them")
	}
	if hasBadEscape(s) {
		return errors.New("invalid percent-encoding")
	}
	// The rest is checked in the canonical form, in which "%2E" is "." and the parameters are sorted by name.
	path, params := splitRedirectFrom(s)
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "." || seg == ".." {
			return errors.New(`dot segments ("." and "..") are not allowed`)
		}
	}
	for i, p := range params {
		switch name := paramName(p); {
		case name == "":
			return errors.New("empty query parameter name")
		case i > 0 && name == paramName(params[i-1]):
			return fmt.Errorf("duplicate query parameter %q", name)
		}
	}
	if c := normalizeRedirectFrom(s); c != s {
		return fmt.Errorf("not in the canonical form %q; normalize it first", c)
	}
	return nil
}

// redirectMatches reports whether a redirect from from applies to target: the path and the query of a request,
// percent-encoded as browsers send them, or a path in the site. Both are compared in the canonical form,
// ignoring a trailing "/" and the fragment of target. The query of target must have every parameter of from
// and may have others, so a from without a query matches any query.
func redirectMatches(from, target string) bool {
	target, _, _ = strings.Cut(target, "#")
	fromPath, fromParams := splitRedirectFrom(from)
	path, params := splitRedirectFrom(target)
	if trimSlash(fromPath) != trimSlash(path) {
		return false
	}
	for _, p := range fromParams {
		if _, ok := slices.BinarySearchFunc(params, p, compareParams); !ok {
			return false
		}
	}
	return true
}

// trimSlash removes a trailing "/" from path, unless path is "/".
func trimSlash(path string) string {
	if path == "/" {
		return path
	}
	return strings.TrimSuffix(path, "/")
}
