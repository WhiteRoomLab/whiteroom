package render

import (
	"html/template"
	"strings"
)

// EncodeURL percent-encodes s, such as an absolute URL, as the renderer encodes the URLs of the data of templates: the
// bytes that are not URL characters, but not a "%" followed by two hex digits, so that s may be encoded already.
func EncodeURL(s string) string {
	return encodeURL(s)
}

// URL returns the URL of p, a path of the site such as "/about/", for the data of templates: with the base of r before
// it, percent-encoded, and trusted as the URLs of the data of blocks are.
func (r *Renderer) URL(p string) template.URL {
	return template.URL(r.sitePath(p))
}

// encodeURL percent-encodes the bytes of s, with upper-case hex digits, except ASCII letters and digits, "-", ".", "_",
// "~", the reserved characters ! # $ & * + , / : ; = ? @ [ ] and a "%" followed by two hex digits. These are the bytes
// that the URL normalizer of html/template keeps, so it leaves the result as it is. Non-ASCII bytes are encoded in
// hosts too, which browsers decode before IDNA.
func encodeURL(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	written := 0
	for i := range len(s) {
		c := s[i]
		if isURLByte(c) || c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]) {
			continue
		}
		b.WriteString(s[written:i])
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
		written = i + 1
	}
	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

// isURLByte reports whether encodeURL keeps c wherever it is.
func isURLByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		strings.IndexByte("-._~!#$&*+,/:;=?@[]", c) >= 0
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// sitePath returns the URL of p, a path of the site such as the Resolver gives.
func (r *Renderer) sitePath(p string) string {
	return encodeURL(r.base + p)
}

// hrefURL returns the URL of href, the href of a link in content, which is a path of the site if it starts with "/" but
// not with "//" (a URL without its scheme).
func (r *Renderer) hrefURL(href string) string {
	if strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") {
		return r.sitePath(href)
	}
	return encodeURL(href)
}
