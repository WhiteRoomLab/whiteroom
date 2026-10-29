package content

import (
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeRedirectFrom(t *testing.T) {
	const (
		ctrl             = string(rune(0x0001))
		rlo              = string(rune(0x202E)) // RIGHT-TO-LEFT OVERRIDE
		zwj              = string(rune(0x200D))
		ideographicSpace = string(rune(0x3000))
		voicedMark       = string(rune(0x3099)) // "か" and voicedMark is "が" in NFD
	)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "Japanese", in: "/%E4%BC%9A%E7%A4%BE%E6%A6%82%E8%A6%81/", want: "/会社概要/"},
		{name: "Japanese in lower case hex", in: "/%e4%bc%9a%e7%a4%be%e6%a6%82%e8%a6%81/", want: "/会社概要/"},
		{name: "Japanese in the query", in: "/?tag=%E4%BC%9A%E7%A4%BE", want: "/?tag=会社"},
		{
			name: "file name of an upload",
			in:   "/wp-content/uploads/2020/05/%E5%86%99%E7%9C%9F-scaled.jpg",
			want: "/wp-content/uploads/2020/05/写真-scaled.jpg",
		},
		{name: "unreserved", in: "/%41%7a%30%2D%2e%5F%7E", want: "/Az0-._~"},
		{name: "reserved and others", in: "/%2F%3F%26%3D%2B%25%20", want: "/%2F%3F%26%3D%2B%25%20"},
		{name: "lower case hex", in: "/%2f%3f%26%3d%2b%25%20?q=%2f%3d", want: "/%2F%3F%26%3D%2B%25%20?q=%2F%3D"},
		{name: "characters to encode", in: "/a b\"<>^`{|}[]", want: "/a%20b%22%3C%3E%5E%60%7B%7C%7D%5B%5D"},
		{name: "characters to encode in the query", in: "/?q=a b\"<>^`{|}[]", want: "/?q=a%20b%22%3C%3E%5E%60%7B%7C%7D%5B%5D"},
		{name: "characters allowed in the path", in: "/a-b_c.d~e!$&'()*+,;=:@/", want: "/a-b_c.d~e!$&'()*+,;=:@/"},
		{name: "characters allowed in the query", in: "/?a=!$()*+,;=:@/?", want: "/?a=!$()*+,;=:@/?"},
		{name: "apostrophe in the query", in: "/it's/?q=it's", want: "/it's/?q=it%27s"},
		{name: "NFD", in: "/cafe" + acute + "/か" + voicedMark, want: "/cafe" + acute + "/か" + voicedMark},
		{name: "NFD encoded", in: "/cafe%CC%81/%E3%81%8B%E3%82%99", want: "/cafe" + acute + "/か" + voicedMark},
		{name: "NFC encoded", in: "/caf%C3%A9", want: "/caf" + eAcute},
		{
			name: "format and space characters",
			in:   "/a" + rlo + "b" + zwj + "c" + ideographicSpace + "d",
			want: "/a%E2%80%AEb%E2%80%8Dc%E3%80%80d",
		},
		{name: "encoded format and space characters", in: "/a%E2%80%AEb%e2%80%8dc%E3%80%80d", want: "/a%E2%80%AEb%E2%80%8Dc%E3%80%80d"},
		{name: "no-break space", in: "/a" + string(rune(0x00A0)) + "b%C2%A0", want: "/a%C2%A0b%C2%A0"},
		{name: "private use and unassigned", in: "/" + string(rune(0xE000)) + string(rune(0x0378)), want: "/%EE%80%80%CD%B8"},
		{name: "invalid byte", in: "/%FF%ff", want: "/%FF%FF"},
		{name: "incomplete character", in: "/%E4%BC/", want: "/%E4%BC/"},
		{name: "overlong slash", in: "/%C0%AF", want: "/%C0%AF"},
		{name: "surrogate", in: "/%ED%A0%80", want: "/%ED%A0%80"},
		{name: "character after an invalid byte", in: "/%FF%E4%BC%9A", want: "/%FF会"},
		{name: "ASCII after a character", in: "/%E4%BC%9A%41%2F", want: "/会A%2F"},
		{name: "replacement character", in: "/%EF%BF%BD", want: "/" + string(rune(0xFFFD))},
		{name: "NUL", in: "/a%00b", want: "/a%00b"},
		{name: "control characters and surrounding spaces", in: " \t/a" + ctrl + "b\n" + ideographicSpace, want: "/ab"},
		{name: "invalid UTF-8", in: "/a\xffb", want: "/a" + string(rune(0xFFFD)) + "b"},
		{name: "query sorted", in: "/?p=2&a=1", want: "/?a=1&p=2"},
		{name: "query sorted by name first", in: "/?a-b=1&a=2", want: "/?a=2&a-b=1"},
		{name: "same names sorted", in: "/?a=2&a=1&a", want: "/?a&a=1&a=2"},
		{name: "query sorted after decoding", in: "/?%62=1&a=2", want: "/?a=2&b=1"},
		{name: "empty parameters", in: "/?a=1&&b=2&", want: "/?a=1&b=2"},
		{name: "empty query", in: "/x?", want: "/x"},
		{name: "query of empty parameters", in: "/x?&&", want: "/x"},
		{name: "trailing slash and upper case", in: "/About/", want: "/About/"},
		{name: "backslash and fragment", in: `/a\b#c`, want: `/a\b#c`},
		{name: "percent without hex digits", in: "/100%", want: "/100%"},
		{name: "escapes after a bad percent", in: "/%E4%BC%9A/%%34%31", want: "/会/%%34%31"},
		{name: "characters with a bad percent", in: "/50% off", want: "/50%%20off"},
		{name: "parts without a bad percent", in: "/%E4%BC%9A?b=%E4%BC%9A&a=50%", want: "/会?a=50%&b=会"},
		{name: "canonical query", in: "/?p=123", want: "/?p=123"},
		{name: "canonical Japanese", in: "/会社/", want: "/会社/"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRedirectFrom(tt.in)
			if got != tt.want {
				t.Errorf("normalizeRedirectFrom(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if again := normalizeRedirectFrom(got); again != got {
				t.Errorf("normalizeRedirectFrom(%q) = %q, want it unchanged", got, again)
			}
		})
	}
}

func TestCheckRedirectFrom(t *testing.T) {
	const (
		normalize = "contains control characters or surrounding spaces; normalize it first"
		path      = `must be a path starting with "/"`
		backslash = "backslashes are not allowed"
		fragment  = "fragments are not allowed; browsers never send them"
		escape    = "invalid percent-encoding"
		dots      = `dot segments ("." and "..") are not allowed`
		duplicate = `duplicate query parameter "a"`
	)
	canonical := func(s string) string { return "not in the canonical form " + strconv.Quote(s) + "; normalize it first" }
	long := "/" + strings.Repeat("a", MaxTextBytes-1)
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "root", in: "/"},
		{name: "path", in: "/about/"},
		{name: "query", in: "/?p=123"},
		{name: "Japanese", in: "/会社/"},
		{name: "file name of an upload", in: "/wp-content/uploads/2020/05/写真-scaled.jpg"},
		{name: "NFD", in: "/cafe" + acute + "/"},
		{name: "upper case without a trailing slash", in: "/About"},
		{name: "encoded reserved characters", in: "/a%2Fb?q=%26%3D%2B%25%20"},
		{name: "sorted parameters", in: "/?cat=3&feed=rss2&p=1"},
		{name: "parameters without values", in: "/?a&b="},
		{name: "dots in segments", in: "/.well-known/a..b/.../"},
		{name: "dots in the query", in: "/?a=..&b=."},
		{name: "empty segment", in: "/a//b"},
		{name: "longest", in: long},

		{name: "too long", in: long + "a", msg: "too long: 1048577 bytes (max 1048576)"},
		{name: "invalid UTF-8", in: "/\xff", msg: "invalid UTF-8"},
		{name: "empty", in: "", msg: path},
		{name: "leading space", in: " /about/", msg: normalize},
		{name: "trailing ideographic space", in: "/about/" + string(rune(0x3000)), msg: normalize},
		{name: "tab", in: "/a\tb", msg: normalize},
		{name: "relative path", in: "about/", msg: path},
		{name: "query without a path", in: "?p=123", msg: path},
		{name: "URL", in: "https://example.com/about/", msg: path},
		{name: "protocol-relative", in: "//evil.example/", msg: "protocol-relative URLs are not allowed"},
		{name: "backslash", in: `/\evil.example`, msg: backslash},
		{name: "backslash in the query", in: `/?a=\`, msg: backslash},
		{name: "fragment", in: "/about/#team", msg: fragment},
		{name: "empty fragment", in: "/about/#", msg: fragment},
		{name: "percent without hex digits", in: "/100%", msg: escape},
		{name: "percent with a hex digit", in: "/%4", msg: escape},
		{name: "percent before an escape", in: "/%%41", msg: escape},
		{name: "bad percent in the query", in: "/?q=%zz", msg: escape},
		{name: "dot segment", in: "/a/./b", msg: dots},
		{name: "double-dot segment", in: "/a/../b", msg: dots},
		{name: "dot segment at the end", in: "/a/..", msg: dots},
		{name: "dot segment before the query", in: "/.?p=1", msg: dots},
		{name: "encoded dot segment", in: "/a/.%2e/b", msg: dots},
		{name: "empty parameter name", in: "/?=1", msg: "empty query parameter name"},
		{name: "empty parameter name after another", in: "/?a=1&=2", msg: "empty query parameter name"},
		{name: "duplicate parameter", in: "/?a=1&a=2", msg: duplicate},
		{name: "duplicate parameter without a value", in: "/?a&a=1", msg: duplicate},
		{name: "duplicate parameter encoded", in: "/?a=1&%61=2", msg: duplicate},
		{name: "encoded Japanese", in: "/%E4%BC%9A%E7%A4%BE/", msg: canonical("/会社/")},
		{name: "lower case hex", in: "/a%2fb", msg: canonical("/a%2Fb")},
		{name: "encoded unreserved", in: "/%7Euser/", msg: canonical("/~user/")},
		{name: "space", in: "/a b", msg: canonical("/a%20b")},
		{name: "format character", in: "/a" + string(rune(0x202E)) + "b", msg: canonical("/a%E2%80%AEb")},
		{name: "unsorted parameters", in: "/?p=2&a=1", msg: canonical("/?a=1&p=2")},
		{name: "empty parameter", in: "/?a=1&&b=2", msg: canonical("/?a=1&b=2")},
		{name: "empty query", in: "/about/?", msg: canonical("/about/")},
		{name: "apostrophe in the query", in: "/?q=it's", msg: canonical("/?q=it%27s")},

		{name: "backslash before a fragment", in: `/#\`, msg: backslash},
		{name: "fragment before a bad percent", in: "/%#", msg: fragment},
		{name: "bad percent before dot segments", in: "/../%", msg: escape},
		{name: "dot segments before parameter names", in: "/./?=1", msg: dots},
		{name: "duplicate parameter before the canonical form", in: "/?b=1&a=1&a=2", msg: duplicate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkRedirectFrom(tt.in), tt.msg)
		})
	}
}

func TestRedirectMatches(t *testing.T) {
	const voicedMark = string(rune(0x3099))
	tests := []struct {
		name   string
		from   string
		target string
		want   bool
	}{
		{name: "same path", from: "/about/", target: "/about/", want: true},
		{name: "without the trailing slash", from: "/about/", target: "/about", want: true},
		{name: "with a trailing slash", from: "/about", target: "/about/", want: true},
		{name: "root", from: "/", target: "/", want: true},
		{name: "root and a path", from: "/", target: "/about/"},
		{name: "two trailing slashes", from: "/about//", target: "/about/"},
		{name: "encoded request", from: "/会社/", target: "/%E4%BC%9A%E7%A4%BE/", want: true},
		{name: "lower case hex", from: "/会社/", target: "/%e4%bc%9a%e7%a4%be/", want: true},
		{name: "encoded from", from: "/%E4%BC%9A%E7%A4%BE/", target: "/会社/", want: true},
		{name: "encoded unreserved", from: "/~user/", target: "/%7Euser/", want: true},
		{name: "space", from: "/a%20b/", target: "/a b/", want: true},
		{name: "NFD", from: "/cafe" + acute + "/", target: "/cafe%CC%81/", want: true},
		{name: "NFC for NFD", from: "/cafe" + acute + "/", target: "/caf%C3%A9/"},
		{name: "NFD for NFC", from: "/caf" + eAcute + "/", target: "/cafe%CC%81/"},
		{name: "NFC kana for NFD", from: "/か" + voicedMark + "/", target: "/%E3%81%8C/"},
		{name: "case", from: "/About/", target: "/about/"},
		{name: "query", from: "/?p=123", target: "/?p=123", want: true},
		{name: "query in another order", from: "/?a=1&p=2", target: "/?p=2&a=1", want: true},
		{name: "extra parameter", from: "/?p=123", target: "/?p=123&utm_source=x", want: true},
		{name: "extra parameter first", from: "/?p=123", target: "/?utm_source=x&p=123", want: true},
		{name: "empty parameters", from: "/?p=123", target: "/?&p=123&", want: true},
		{name: "repeated parameter", from: "/?a=2", target: "/?a=1&a=2", want: true},
		{name: "missing parameter", from: "/?a=1&p=2", target: "/?p=2"},
		{name: "other value", from: "/?p=123", target: "/?p=1234"},
		{name: "other name", from: "/?p=123", target: "/?page_id=123"},
		{name: "without the query", from: "/?p=123", target: "/"},
		{name: "query with another path", from: "/?p=123", target: "/about/?p=123"},
		{name: "from without a query", from: "/about/", target: "/about/?x=1", want: true},
		{name: "plus and %20", from: "/?q=a+b", target: "/?q=a%20b"},
		{name: "%2F and slash", from: "/a%2Fb", target: "/a/b"},
		{name: "apostrophe in the query", from: "/?q=it%27s", target: "/?q=it's", want: true},
		{name: "fragment", from: "/about/", target: "/about/#team", want: true},
		{name: "query after the fragment", from: "/?p=123", target: "/#?p=123"},
		{name: "other path", from: "/about/", target: "/contact/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redirectMatches(tt.from, tt.target); got != tt.want {
				t.Errorf("redirectMatches(%q, %q) = %t, want %t", tt.from, tt.target, got, tt.want)
			}
		})
	}
}
