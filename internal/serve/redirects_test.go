package serve

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// testRedirects returns a redirect from each of froms, which must be in the canonical form, to a path of its own.
func testRedirects(t *testing.T, froms ...string) []serverRedirect {
	t.Helper()
	rs := make([]serverRedirect, len(froms))
	for i, f := range froms {
		if c := content.CanonicalFrom(f); c != f {
			t.Fatalf("from %q is not in the canonical form %q", f, c)
		}
		rs[i] = serverRedirect{From: f, To: "/to/" + strconv.Itoa(i) + "/", Status: 301}
	}
	return rs
}

// fromOf returns the from of r; "" for nil.
func fromOf(r *serverRedirect) string {
	if r == nil {
		return ""
	}
	return r.From
}

// TestMatch checks that match finds the redirect that applies to a request as 6.6.6 says: the paths and the queries in
// the canonical form, a trailing "/" either way, the parameters in any order and with others, the redirect with the
// most parameters and then the smallest from, and nothing after "#".
func TestMatch(t *testing.T) {
	x := newRedirectIndex(testRedirects(t,
		"/?a=1", "/?a=1&b=2", "/a/", "/?e", "/?k=a%2Bb", "/旧ページ/", "/a%2Fb/", "/a+b/", "/$var/", "/?p=123", "/?q=新",
		"/news/?r=1",
	))
	for _, tt := range []struct{ target, from string }{
		// The redirect with more parameters applies, though "/?a=1" is the smaller.
		{"/?b=2&a=1", "/?a=1&b=2"},
		{"/?a=1&c=3&b=2", "/?a=1&b=2"},
		{"/?a=1", "/?a=1"},
		{"/?b=3&a=1", "/?a=1"},
		{"/?b=2", ""},
		{"/?A=1", ""},
		{"/?a", ""},
		{"/?a=1%26b=2", ""},
		{"/", ""},
		{"/a", "/a/"},
		{"/a/", "/a/"},
		{"/a/?x=1", "/a/"},
		{"/a#x", "/a/"},
		{"/A/", ""},
		{"/a//", ""},
		{"/?e", "/?e"},
		{"/?e=", ""},
		{"/?z=1&e", "/?e"},
		// Of as many parameters, the smaller from.
		{"/?k=a%2Bb&e", "/?e"},
		{"/?k=a%2bb", "/?k=a%2Bb"},
		{"/?k=a+b", ""},
		{"/%E6%97%A7%E3%83%9A%E3%83%BC%E3%82%B8", "/旧ページ/"},
		{"/%e6%97%a7%e3%83%9a%e3%83%bc%e3%82%b8/?x=1", "/旧ページ/"},
		{"/旧ページ", "/旧ページ/"},
		{"/a/b/", ""},
		{"/a%2Fb/", "/a%2Fb/"},
		{"/a%2fb", "/a%2Fb/"},
		{"/a%2Bb/", ""},
		{"/a+b", "/a+b/"},
		{"/%24var", ""},
		{"/$var", "/$var/"},
		{"/?utm=1&p=123", "/?p=123"},
		{"/?p=123#x", "/?p=123"},
		{"/?p=1234", ""},
		{"/index.php?p=123", ""},
		{"/?q=%e6%96%b0", "/?q=新"},
		{"/?q=%E6%96%B0", "/?q=新"},
		{"/news?r=1", "/news/?r=1"},
		{"/news/?x=0&r=1", "/news/?r=1"},
		{"/news/", ""},
	} {
		if got := fromOf(x.match(tt.target)); got != tt.from {
			t.Errorf("match(%q) = the redirect from %q, want %q", tt.target, got, tt.from)
		}
	}
}

// TestMatchTies checks that of the redirects that match with as many parameters, the one with the smaller from applies,
// and of those with the same from, the first.
func TestMatchTies(t *testing.T) {
	rs := testRedirects(t, "/b/", "/b", "/b?x=1", "/b/?x=1", "/c/", "/c/")
	x := newRedirectIndex(rs)
	for _, tt := range []struct {
		target string
		want   *serverRedirect
	}{
		{"/b", &rs[1]},
		{"/b/", &rs[1]},
		{"/b/?x=1", &rs[3]},
		{"/b?y=2&x=1", &rs[3]},
		{"/b?x=2", &rs[1]},
		{"/c", &rs[4]},
		{"/c/?x=1", &rs[4]},
	} {
		if got := x.match(tt.target); got != tt.want {
			t.Errorf("match(%q) = %+v, want %+v", tt.target, got, *tt.want)
		}
	}
}

// scan returns the redirect of rs that applies to target, as match must find it, by trying each of them.
func scan(rs []serverRedirect, target string) *serverRedirect {
	var best *serverRedirect
	for i := range rs {
		r := &rs[i]
		if (content.Redirect{From: r.From}).Matches(target) && (best == nil || comparePrecedence(r.From, best.From) < 0) {
			best = r
		}
	}
	return best
}

// trickyFroms returns froms in the canonical form, without duplicates, that differ in what the canonical form keeps
// apart, or share their paths or their parameters with others.
func trickyFroms() []string {
	const (
		ideographicSpace = string(rune(0x3000))
		combiningDakuten = string(rune(0x3099))
	)
	raw := []string{
		"/", "/a", "/a/", "/A/", "/a/b", "/a/b/", "/a//b/", "/a%2Fb/", "/a%2fb", "/a+b/", "/a%2Bb/", "/a%20b/", "/a b/",
		"/$var/", "/%24var/", "/a(b)/", "/a%28b%29/", "/it's/", "/it%27s/", "/a;b", "/a=b/", "/a:b/", "/a@b/", "/a~b/",
		"/a%7Eb/", "/%41/", "/旧ページ/", "/%E6%97%A7%E3%83%9A%E3%83%BC%E3%82%B8", "/会社/", "/か" + combiningDakuten + "/",
		"/が/", "/tag/a" + ideographicSpace + "b/", "/%FF/", "/a%01b/", "/.htaccess", "/old.html", "/old%2Ehtml/",
		"/a.b/", "/2024/04/01/slug/", "/wp-content/uploads/2024/04/会社案内.pdf", "/a#b", `/a\b`, "/./", "/a/./b",
		"/%2E%2E/", "/x/y/z/", "/x/y/", "/x/",
		"/?p=1", "/?p=12", "/?p=123", "/?P=123", "/?a=1", "/?a=2", "/?a=1&b=2", "/?b=2&a=1", "/?a=1&b=2&c=3", "/?b=2",
		"/?c=3", "/?e", "/?e=", "/?k=a%2Bb", "/?k=a+b", "/?k=a%2bb", "/?q=新", "/?q=%E6%96%B0", "/?x=$y", "/?x=%24y",
		"/?a=b=c", "/?a=1?b", "/?url=/x/", "/?u='x'", "/?a=1&&b=2", "/?", "/?&", "/news/?r=1", "/news?r=1",
		"/news/?r=1&s=2", "/a/?x=1", "/a?x=1", "/a?x=1&y=2", "/a/?y=2", "/index.php?p=123", "/?p=123&p2=1", "/?a=1#x",
		"/?a", "/?a&b", "/?ab", "/?a=1&z", "/?z", "/旧ページ/?a=1", "/?page_id=5", "/?cat=3&paged=2", "/?m=202401",
		"/category/news/?paged=2", "/%E4%BC%9A%E7%A4%BE/?a=%E6%96%B0", "/?a=%2F", "/?a=/", "/?a=%26", "/?a=%3D",
		"/?a==", "/?=1", "/?a=1&a=2",
	}
	var froms []string
	for _, s := range raw {
		if c := content.CanonicalFrom(s); !slices.Contains(froms, c) {
			froms = append(froms, c)
		}
	}
	return froms
}

// variants returns requests like from, a from in the canonical form, that may or may not match it: from itself; with
// the trailing "/" of its path added or removed; with another parameter; with the parameters reversed; without the
// first; with the hex digits in lower case; with the bytes of the characters other than ASCII percent-encoded, as
// browsers send them; with "#..."; under another path; and with a parameter changed.
func variants(from string) []string {
	path, query, _ := strings.Cut(from, "?")
	var params []string
	if query != "" {
		params = strings.Split(query, "&")
	}
	join := func(path string, params []string) string {
		if len(params) == 0 {
			return path
		}
		return path + "?" + strings.Join(params, "&")
	}
	slash := path + "/"
	if path != "/" && strings.HasSuffix(path, "/") {
		slash = strings.TrimSuffix(path, "/")
	}
	reversed := slices.Clone(params)
	slices.Reverse(reversed)
	vs := []string{
		from,
		join(slash, params),
		join(path, append(slices.Clone(params), "utm=1")),
		join(path, reversed),
		lowerHex(from),
		browserForm(from),
		from + "#x",
		"/x" + from,
	}
	if len(params) > 0 {
		vs = append(vs, join(path, params[1:]), join(path, append([]string{params[0] + "0"}, params[1:]...)))
	}
	return vs
}

// lowerHex returns s with the hex digits of its percent-encoding in lower case.
func lowerHex(s string) string {
	b := []byte(s)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' {
			b[i+1], b[i+2] = lower(b[i+1]), lower(b[i+2])
		}
	}
	return string(b)
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'F' {
		return c + 'a' - 'A'
	}
	return c
}

// browserForm returns s with the bytes of the characters other than ASCII percent-encoded in upper case.
func browserForm(s string) string {
	var b strings.Builder
	for i := range len(s) {
		if c := s[i]; c >= utf8.RuneSelf {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// checkScan checks that match finds the redirect of rs that scan finds for each of targets, and returns the number of
// the targets that a redirect applies to.
func checkScan(t *testing.T, rs []serverRedirect, targets []string) int {
	t.Helper()
	x := newRedirectIndex(rs)
	n := 0
	for _, target := range targets {
		got, want := x.match(target), scan(rs, target)
		if got != want {
			t.Errorf("match(%q) = the redirect from %q (%p), want that from %q (%p)", target, fromOf(got), got,
				fromOf(want), want)
		}
		if want != nil {
			n++
		}
	}
	return n
}

// withoutRoot returns froms without "/", which matches every request for "/" whatever its query.
func withoutRoot(froms []string) []string {
	return slices.DeleteFunc(slices.Clone(froms), func(f string) bool { return f == "/" })
}

// TestMatchScan checks match against scan with trickyFroms and their variants, with and without "/".
func TestMatchScan(t *testing.T) {
	froms := trickyFroms()
	targets := []string{
		"", "/", "/?", "/?&", "/?=", "/#", "/?#", "/?a=1&a=2&b=2", "/?p=1&p=12", "/%", "/%zz", "/a%", "/?a=%zz",
	}
	for _, f := range froms {
		targets = append(targets, variants(f)...)
	}
	for _, froms := range [][]string{froms, withoutRoot(froms)} {
		if n := checkScan(t, testRedirects(t, froms...), targets); n < len(targets)/2 || n == len(targets) {
			t.Errorf("redirects apply to %d of %d targets", n, len(targets))
		}
	}
}

// TestMatchScanMany checks match against scan with tens of thousands of redirects at one path, like those of the
// posts of a site from WordPress, besides trickyFroms with and without "/".
func TestMatchScanMany(t *testing.T) {
	froms := trickyFroms()
	for n := range 20000 {
		froms = append(froms, "/?p="+strconv.Itoa(n))
	}
	targets := []string{
		"/?p=123", "/?p=123&utm=1", "/?utm=1&p=19999", "/?p=20000", "/?p=0123", "/?P=1", "/?p=1&p=2", "/?p=5&a=1&b=2",
		"/", "/?", "/?p=", "/?p", "/index.php?p=1", "/?p=1#x", "/?p=%31", "/?p=1%20", "/a/?p=1", "/?e&p=7",
		"/?q=%e6%96%b0&p=8", "/?p=19999&p=0",
	}
	for _, froms := range [][]string{froms, withoutRoot(froms)} {
		if n := checkScan(t, testRedirects(t, froms...), targets); n < len(targets)/2 || n == len(targets) {
			t.Errorf("redirects apply to %d of %d targets", n, len(targets))
		}
	}
}
