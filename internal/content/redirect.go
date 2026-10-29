package content

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

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
