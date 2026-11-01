package render

import (
	"fmt"
	"html"
	"html/template"
	"strings"
	"testing"
)

const (
	urlBytes  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~!#$&*+,/:;=?@[]" // that encodeURL keeps
	hexDigits = "0123456789ABCDEFabcdef"
)

var hrefTemplate = template.Must(template.New("href").Parse(`<a href="{{.}}">`))

// normalized returns s as html/template writes it in an href as a template.URL, without the escaping of HTML (which
// writes "+" as "&#43;"): s if the URL normalizer of html/template keeps it.
func normalized(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	if err := hrefTemplate.Execute(&b, template.URL(s)); err != nil {
		t.Fatalf("Execute(%q) error: %v", s, err)
	}
	return html.UnescapeString(strings.TrimSuffix(strings.TrimPrefix(b.String(), `<a href="`), `">`))
}

// checkEncoded checks out, which encodeURL returned for in: encodeURL keeps it, it has only urlBytes and "%" before two
// hex digits, and html/template keeps it.
func checkEncoded(t *testing.T, in, out string) {
	t.Helper()
	if again := encodeURL(out); again != out {
		t.Errorf("encodeURL(%q) = %q, which it encodes again to %q", in, out, again)
	}
	isHexDigit := func(i int) bool { return i < len(out) && strings.IndexByte(hexDigits, out[i]) >= 0 }
	for i := range len(out) {
		c := out[i]
		if c == '%' && isHexDigit(i+1) && isHexDigit(i+2) {
			continue
		}
		if strings.IndexByte(urlBytes, c) < 0 {
			t.Errorf("encodeURL(%q) = %q, which has %q at %d", in, out, c, i)
			break
		}
	}
	if got := normalized(t, out); got != out {
		t.Errorf("encodeURL(%q) = %q, which html/template writes as %q", in, out, got)
	}
}

func TestEncodeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"/about/", "/about/"},
		{"/日記/", "/%E6%97%A5%E8%A8%98/"},
		{"/a b/", "/a%20b/"},
		{`"`, "%22"},
		{"'", "%27"},
		{"(", "%28"},
		{")", "%29"},
		{"<", "%3C"},
		{">", "%3E"},
		{`\`, "%5C"},
		{"^", "%5E"},
		{"`", "%60"},
		{"{", "%7B"},
		{"|", "%7C"},
		{"}", "%7D"},
		{"!#$&*+,/:;=?@[]", "!#$&*+,/:;=?@[]"},
		{"-._~", "-._~"},
		{"%", "%25"},
		{"%4", "%254"},
		{"%zz", "%25zz"},
		{"%%41", "%25%41"},
		{"%41%e6", "%41%e6"},
		{"100%", "100%25"},
		{"\x00", "%00"},
		{"\x1f", "%1F"},
		{"\x7f", "%7F"},
		{"\xff", "%FF"},
		{"https://例え.jp/パス", "https://%E4%BE%8B%E3%81%88.jp/%E3%83%91%E3%82%B9"},
		{`javascript:alert("x")`, "javascript:alert%28%22x%22%29"},
		// U+3000, which slugs may have, before a path that is encoded already.
		{"https://example.com/a" + string(rune(0x3000)) + "b/%E3%81%82/", "https://example.com/a%E3%80%80b/%E3%81%82/"},
	}
	for _, tt := range tests {
		got := encodeURL(tt.in)
		if got != tt.want {
			t.Errorf("encodeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if exported := EncodeURL(tt.in); exported != got {
			t.Errorf("EncodeURL(%q) = %q, want %q", tt.in, exported, got)
		}
		checkEncoded(t, tt.in, got)
	}
	for c := range 256 {
		in := string([]byte{byte(c)})
		want := fmt.Sprintf("%%%02X", c)
		if strings.IndexByte(urlBytes, byte(c)) >= 0 {
			want = in
		}
		got := encodeURL(in)
		if got != want {
			t.Errorf("encodeURL(%q) = %q, want %q", in, got, want)
		}
		checkEncoded(t, in, got)
		// A "%" stays only before two hex digits.
		in = "%" + in + "f"
		want = "%25" + want + "f"
		if strings.IndexByte(hexDigits, byte(c)) >= 0 {
			want = in
		}
		if got := encodeURL(in); got != want {
			t.Errorf("encodeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSiteURLs(t *testing.T) {
	const (
		pass = "/%E3%83%91%E3%82%B9/"
		blog = "/%E3%83%96%E3%83%AD%E3%82%B0"
	)
	paths := []string{"/", "/about/", "/?p=123", "/パス/"}
	tests := []struct {
		base string
		want []string // for each of paths
	}{
		{"", []string{"/", "/about/", "/?p=123", pass}},
		{"/blog", []string{"/blog/", "/blog/about/", "/blog/?p=123", "/blog" + pass}},
		{"https://example.com/blog", []string{
			"https://example.com/blog/", "https://example.com/blog/about/", "https://example.com/blog/?p=123",
			"https://example.com/blog" + pass,
		}},
		{"/ブログ", []string{blog + "/", blog + "/about/", blog + "/?p=123", blog + pass}},
	}
	// The same with every base.
	hrefs := []struct{ in, want string }{
		{"https://example.com/パス?q=a b", "https://example.com/%E3%83%91%E3%82%B9?q=a%20b"},
		{"http://example.com/", "http://example.com/"},
		{
			"mailto:info@example.com?subject=お問い合わせ",
			"mailto:info@example.com?subject=%E3%81%8A%E5%95%8F%E3%81%84%E5%90%88%E3%82%8F%E3%81%9B",
		},
		{"tel:+81-3-1234-5678", "tel:+81-3-1234-5678"},
		// Not a path of the site, which CheckHref rejects anyway.
		{"//example.com/x", "//example.com/x"},
	}
	th := testTheme(t, nil)
	for _, tt := range tests {
		r := newRenderer(t, th, "ja", tt.base, nil)
		for i, p := range paths {
			if got := r.sitePath(p); got != tt.want[i] {
				t.Errorf("base %q: sitePath(%q) = %q, want %q", tt.base, p, got, tt.want[i])
			}
			if got := r.URL(p); string(got) != tt.want[i] {
				t.Errorf("base %q: URL(%q) = %q, want %q", tt.base, p, got, tt.want[i])
			}
			if got := r.hrefURL(p); got != tt.want[i] {
				t.Errorf("base %q: hrefURL(%q) = %q, want %q", tt.base, p, got, tt.want[i])
			}
		}
		for _, h := range hrefs {
			if got := r.hrefURL(h.in); got != h.want {
				t.Errorf("base %q: hrefURL(%q) = %q, want %q", tt.base, h.in, got, h.want)
			}
		}
	}
}
