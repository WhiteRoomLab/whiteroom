package content

import (
	"cmp"
	"encoding/json/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// testRedirect is the ID of the redirect in testdata.
const testRedirect = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"

// wordpressRedirect is testdata/wordpress-p-redirect.json, which forwards post 123 in the plain permalinks of WordPress
// to the imported post.
var wordpressRedirect = Redirect{
	ID:     testRedirect,
	From:   "/?p=123",
	To:     testPost,
	Status: 301,
}

func TestRedirectUnmarshalJSON(t *testing.T) {
	in := readTestdata(t, "wordpress-p-redirect.json")
	var got Redirect
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	if got != wordpressRedirect {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, wordpressRedirect)
	}
}

func TestRedirectRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "WordPress", in: readTestdata(t, "wordpress-p-redirect.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "reversed key order",
			in:   `{"status":302,"to":"/category/news/page/2/","from":"/?cat=3&paged=2","id":"` + testRedirect + `","schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testRedirect + `","from":"/?cat=3&paged=2","to":"/category/news/page/2/","status":302}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Redirect](t, tt.in, want)
		})
	}
}

func TestRedirectMarshalJSON(t *testing.T) {
	got, err := json.Marshal(Redirect{})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"schemaVersion":1}`; string(got) != want {
		t.Errorf("Marshal(Redirect{}) = %s, want %s", got, want)
	}
}

func TestRedirectUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "missing schemaVersion", in: `{"id":"x","from":"x","to":"x","status":301}`, path: "", msg: `missing key "schemaVersion"`},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "unknown key", in: `{"schemaVersion":1,"language":"ja"}`, path: "/language", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"From":"/?p=123"}`, path: "/From", msg: "unknown key"},
		{name: "status as a string", in: `{"schemaVersion":1,"status":"301"}`, path: "/status", msg: "expected number, got string"},
		{name: "status with a fraction", in: `{"schemaVersion":1,"status":301.0}`, path: "/status", msg: "must be an integer"},
		{name: "status with an exponent", in: `{"schemaVersion":1,"status":3.01e2}`, path: "/status", msg: "must be an integer"},
		{name: "status 0", in: `{"schemaVersion":1,"status":0}`, path: "/status", msg: "unsupported status 0 (use 301 or 302)"},
		{name: "status 303", in: `{"schemaVersion":1,"status":303}`, path: "/status", msg: "unsupported status 303 (use 301 or 302)"},
		{name: "status 308", in: `{"schemaVersion":1,"status":308}`, path: "/status", msg: "unsupported status 308 (use 301 or 302)"},
		{name: "status 410", in: `{"schemaVersion":1,"status":410}`, path: "/status", msg: "unsupported status 410 (use 301 or 302)"},
		{name: "negative status", in: `{"schemaVersion":1,"status":-301}`, path: "/status", msg: "unsupported status -301 (use 301 or 302)"},
		{name: "null from", in: `{"schemaVersion":1,"from":null}`, path: "/from", msg: "expected string, got null"},
		{name: "empty from", in: `{"schemaVersion":1,"from":""}`, path: "/from", msg: "empty string is not allowed"},
		{name: "link for to", in: `{"schemaVersion":1,"to":{"entry":"` + testPost + `"}}`, path: "/to", msg: "expected string, got object"},
		{name: "duplicate key", in: `{"schemaVersion":1,"to":"/a/","to":"/b/"}`, path: "/to", msg: "duplicate key"},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redirect{ID: "old"}
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if got != (Redirect{ID: "old"}) {
				t.Errorf("UnmarshalJSON(%s) changed the redirect to %#v", tt.in, got)
			}
		})
	}
}

func TestRedirectNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	tests := []struct {
		name string
		in   Redirect
		want Redirect
	}{
		{
			name: "from in the canonical form",
			in:   Redirect{From: " /%E4%BC%9A%E7%A4%BE/?p=2&a=1" + ctrl + "\n"},
			want: Redirect{From: "/会社/?a=1&p=2"},
		},
		{name: "from not in NFC", in: Redirect{From: "/cafe" + acute + "/"}, want: Redirect{From: "/cafe" + acute + "/"}},
		{
			name: "spaces and control characters in to",
			in:   Redirect{To: " https://example.com/ab\tout/" + ctrl + "\n"},
			want: Redirect{To: "https://example.com/about/"},
		},
		{name: "to not in NFC", in: Redirect{To: "/cafe" + acute + "/"}, want: Redirect{To: "/cafe" + acute + "/"}},
		{name: "encoded to left as it is", in: Redirect{To: "/%E4%BC%9A%E7%A4%BE/"}, want: Redirect{To: "/%E4%BC%9A%E7%A4%BE/"}},
		{name: "ID in to", in: Redirect{To: "\t" + testPost + " "}, want: Redirect{To: testPost}},
		{name: "nothing left", in: Redirect{From: " " + ctrl, To: "\n"}, want: Redirect{}},
		{
			name: "other items left as they are",
			in:   Redirect{ID: " x" + ctrl, Status: 303},
			want: Redirect{ID: " x" + ctrl, Status: 303},
		},
		{name: "WordPress", in: wordpressRedirect, want: wordpressRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.Normalize()
			if got != tt.want {
				t.Errorf("Normalize() of %#v = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedirectValidate(t *testing.T) {
	const (
		normalize = "contains control characters or surrounding spaces; normalize it first"
		notWeb    = `/to: must be a path starting with "/" or an http or https URL`
		itself    = "/to: redirects to itself"
	)
	invalidTarget := func(s string) string {
		return "/to: invalid target " + strconv.Quote(s) + ": use the ID of an entry, a term, an author or a medium, " +
			`a path starting with "/", or an http or https URL`
	}
	// redirect returns x changed by f.
	redirect := func(x Redirect, f func(x *Redirect)) Redirect {
		f(&x)
		return x
	}
	from := func(s string) Redirect { return redirect(wordpressRedirect, func(x *Redirect) { x.From = s }) }
	to := func(s string) Redirect { return redirect(wordpressRedirect, func(x *Redirect) { x.To = s }) }
	fromTo := func(path, target string) Redirect {
		return redirect(wordpressRedirect, func(x *Redirect) { x.From, x.To = path, target })
	}
	// siteAt returns testSite, whose URL is "https://example.com", with url as its URL.
	siteAt := func(url string) *Site {
		s := testSite()
		s.URL = url
		return s
	}
	blog := siteAt("https://example.com/blog")
	tests := []struct {
		name     string
		site     *Site // testSite() if nil
		in       Redirect
		noLookup bool // validate without lookup
		paths    []string
		msg      string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "WordPress", in: wordpressRedirect},
		{name: "WordPress without lookup", in: wordpressRedirect, noLookup: true},
		{
			name:  "missing items",
			in:    Redirect{},
			paths: []string{"/id", "/from", "/to", "/status"},
			msg:   "/id: missing id\n/from: missing from\n/to: missing to\n/status: missing status",
		},
		{
			// The errors are in document order.
			name:  "invalid items",
			in:    Redirect{ID: testPost, From: "/?p=123&cat=3", To: "about/", Status: 303},
			paths: []string{"/id", "/from", "/to", "/status"},
			msg: `/id: invalid redirect ID "` + testPost + `": use "rdr_" and a ULID (26 characters in upper case)` + "\n" +
				`/from: not in the canonical form "/?cat=3&p=123"; normalize it first` + "\n" +
				invalidTarget("about/") + "\n/status: unsupported status 303 (use 301 or 302)",
		},
		{
			name:  "ID of another kind",
			in:    redirect(wordpressRedirect, func(x *Redirect) { x.ID = testHeaderMenu }),
			paths: []string{"/id"},
			msg:   `/id: invalid redirect ID "` + testHeaderMenu + `": use "rdr_" and a ULID (26 characters in upper case)`,
		},

		{name: "Japanese from", in: from("/会社概要/")},
		{name: "from not in NFC", in: from("/cafe" + acute + "/")},
		{
			name:  "encoded from",
			in:    from("/%E4%BC%9A%E7%A4%BE%E6%A6%82%E8%A6%81/"),
			paths: []string{"/from"},
			msg:   `/from: not in the canonical form "/会社概要/"; normalize it first`,
		},
		{name: "from with surrounding spaces", in: from(" /?p=123"), paths: []string{"/from"}, msg: "/from: " + normalize},
		{name: "URL for from", in: from("https://example.com/?p=123"), paths: []string{"/from"}, msg: `/from: must be a path starting with "/"`},
		{
			name:  "from with a fragment",
			in:    from("/about/#team"),
			paths: []string{"/from"},
			msg:   "/from: fragments are not allowed; browsers never send them",
		},
		{name: "from with a duplicate parameter", in: from("/?p=123&p=124"), paths: []string{"/from"}, msg: `/from: duplicate query parameter "p"`},

		{name: "entry", in: to(testAboutPage)},
		{name: "term", in: fromTo("/?cat=3", testCategory)},
		{name: "author", in: fromTo("/?author=2", testAuthor)},
		{name: "image", in: fromTo("/wp-content/uploads/2020/05/写真-scaled.jpg", testImage)},
		{name: "PDF", in: fromTo("/wp-content/uploads/2020/05/会社案内.pdf", testPDF)},
		{
			name:  "entry that does not exist",
			in:    to("ent_" + testMissing),
			paths: []string{"/to"},
			msg:   `/to: entry "ent_` + testMissing + `" does not exist`,
		},
		{name: "entry that does not exist without lookup", in: to("ent_" + testMissing), noLookup: true},
		{
			name:  "medium that does not exist",
			in:    to("med_" + testMissing),
			paths: []string{"/to"},
			msg:   `/to: media "med_` + testMissing + `" does not exist`,
		},
		{
			name:  "invalid entry ID",
			in:    to("ent_01jb8y6t2wt6x9c3e6g9j2k5m8"),
			paths: []string{"/to"},
			msg:   `/to: invalid entry ID "ent_01jb8y6t2wt6x9c3e6g9j2k5m8": use "ent_" and a ULID (26 characters in upper case)`,
		},
		{name: "ID with surrounding spaces", in: to(" " + testPost), paths: []string{"/to"}, msg: `/to: invalid entry ID " ` + testPost + `"`},
		{name: "revision", in: to(testRevision), paths: []string{"/to"}, msg: invalidTarget(testRevision)},
		{name: "translation group", in: to(testGroup), paths: []string{"/to"}, msg: invalidTarget(testGroup)},
		{name: "login account", in: to(testUser), paths: []string{"/to"}, msg: invalidTarget(testUser)},
		{name: "form", in: to(testForm), paths: []string{"/to"}, msg: invalidTarget(testForm)},
		{name: "menu", in: to(testHeaderMenu), paths: []string{"/to"}, msg: invalidTarget(testHeaderMenu)},
		{name: "redirect", in: to(testRedirect), paths: []string{"/to"}, msg: invalidTarget(testRedirect)},
		{
			name:  "upper case prefix",
			in:    to("ENT_01JB8Y6T2WT6X9C3E6G9J2K5M8"),
			paths: []string{"/to"},
			msg:   invalidTarget("ENT_01JB8Y6T2WT6X9C3E6G9J2K5M8"),
		},
		{name: "relative path", in: to("about/"), paths: []string{"/to"}, msg: invalidTarget("about/")},

		{name: "path", in: to("/about/")},
		{name: "path with a fragment", in: to("/about/#access")},
		{name: "external URL", in: to("https://example.org/")},
		{name: "http URL", in: to("http://example.com/about/")},
		{name: "mailto", in: to("mailto:info@example.com"), paths: []string{"/to"}, msg: notWeb},
		{name: "tel", in: to("tel:+81-3-1234-5678"), paths: []string{"/to"}, msg: notWeb},
		{name: "javascript", in: to("javascript:alert(1)"), paths: []string{"/to"}, msg: `/to: scheme "javascript" is not allowed`},
		{name: "protocol-relative", in: to("//evil.example/"), paths: []string{"/to"}, msg: "/to: protocol-relative URLs are not allowed"},
		{name: "path with surrounding spaces", in: to(" /about/"), paths: []string{"/to"}, msg: "/to: " + normalize},

		{name: "itself without the trailing slash", in: fromTo("/about/", "/about"), paths: []string{"/to"}, msg: itself},
		{name: "itself with a query", in: fromTo("/?p=1", "/?p=1"), paths: []string{"/to"}, msg: itself},
		{name: "root from a query", in: fromTo("/?p=1", "/")},
		{name: "itself with a fragment", in: fromTo("/about/", "/about/#team"), paths: []string{"/to"}, msg: itself},
		{name: "itself with another parameter", in: fromTo("/about/", "/about/?ref=top"), paths: []string{"/to"}, msg: itself},
		{name: "itself encoded", in: fromTo("/会社/", "/%E4%BC%9A%E7%A4%BE/"), paths: []string{"/to"}, msg: itself},
		{name: "upper case path", in: fromTo("/About/", "/about/")},
		{
			// Only From is reported until it is in the canonical form.
			name:  "itself from a from not in the canonical form",
			in:    fromTo("/about/?", "/about/"),
			paths: []string{"/from"},
			msg:   `/from: not in the canonical form "/about/"; normalize it first`,
		},
		{name: "external URL with the same path", in: fromTo("/about/", "https://example.org/about/")},

		{name: "itself as a URL", in: fromTo("/about", "https://example.com/about/"), paths: []string{"/to"}, msg: itself},
		{
			name:  "itself as a URL with the host in upper case",
			in:    fromTo("/about", "https://EXAMPLE.com/about"),
			paths: []string{"/to"},
			msg:   itself,
		},
		{
			name:  "itself as a URL with the scheme in upper case",
			in:    fromTo("/about/", "HTTPS://example.com/about/"),
			paths: []string{"/to"},
			msg:   itself,
		},
		{
			name:  "itself as a URL with another parameter",
			in:    fromTo("/about", "https://example.com/about/?ref=top"),
			paths: []string{"/to"},
			msg:   itself,
		},
		{name: "itself as a URL with a query", in: fromTo("/?p=123", "https://example.com/?p=123"), paths: []string{"/to"}, msg: itself},
		{name: "itself as a URL without a path", in: fromTo("/", "https://example.com"), paths: []string{"/to"}, msg: itself},
		{name: "itself as a URL with a fragment", in: fromTo("/about/", "https://example.com/about/#team"), paths: []string{"/to"}, msg: itself},
		{name: "itself as a URL encoded", in: fromTo("/会社/", "https://example.com/%e4%bc%9a%e7%a4%be/"), paths: []string{"/to"}, msg: itself},
		{
			name:  "itself as a URL with the default port",
			in:    fromTo("/about/", "https://example.com:443/about/"),
			paths: []string{"/to"},
			msg:   itself,
		},
		{name: "root URL from a query", in: fromTo("/?p=1", "https://example.com")},
		{name: "URL with another scheme", in: fromTo("/about/", "http://example.com/about/")},
		{name: "URL with another host", in: fromTo("/about/", "https://www.example.com/about/")},
		{name: "URL with another port", in: fromTo("/about/", "https://example.com:8443/about/")},
		{name: "URL with another path", in: fromTo("/about/", "https://example.com/other/")},
		{
			name:  "itself as a URL in a site at a path",
			site:  blog,
			in:    fromTo("/about/", "https://example.com/blog/about/"),
			paths: []string{"/to"},
			msg:   itself,
		},
		{
			name:  "itself as the URL of a site at a path",
			site:  blog,
			in:    fromTo("/", "https://example.com/blog"),
			paths: []string{"/to"},
			msg:   itself,
		},
		{name: "URL outside a site at a path", site: blog, in: fromTo("/about/", "https://example.com/about/")},
		{name: "URL outside a site at a path that it starts with", site: blog, in: fromTo("/about/", "https://example.com/blogger/about/")},
		{name: "URL in a site without a URL", site: siteAt(""), in: fromTo("/about/", "https://example.com/about/")},
		{name: "URL in a site with an invalid URL", site: siteAt("https://example.com/"), in: fromTo("/about/", "https://example.com/about/")},

		{name: "status 302", in: redirect(wordpressRedirect, func(x *Redirect) { x.Status = 302 })},
		{
			name:  "missing status",
			in:    redirect(wordpressRedirect, func(x *Redirect) { x.Status = 0 }),
			paths: []string{"/status"},
			msg:   "/status: missing status",
		},
		{
			name:  "status 303",
			in:    redirect(wordpressRedirect, func(x *Redirect) { x.Status = 303 }),
			paths: []string{"/status"},
			msg:   "/status: unsupported status 303 (use 301 or 302)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := testLookup
			if tt.noLookup {
				lookup = nil
			}
			err := tt.in.Validate(cmp.Or(tt.site, testSite()), lookup)
			if tt.paths == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("Validate() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}

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
		{name: "first and last letters and digits", in: "/%41%5A%61%7A%30%39/AZaz09", want: "/AZaz09/AZaz09"},
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

func TestCanonicalFrom(t *testing.T) {
	const ideographicSpace = string(rune(0x3000))
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "encoded path and unsorted query", in: "/%E4%BC%9A/?b=2&a=1", want: "/会/?a=1&b=2"},
		{name: "path of a page with an ideographic space", in: "/tag/a" + ideographicSpace + "b/", want: "/tag/a%E3%80%80b/"},
		{name: "canonical", in: "/会社/?p=123", want: "/会社/?p=123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalFrom(tt.in); got != tt.want {
				t.Errorf("CanonicalFrom(%q) = %q, want %q", tt.in, got, tt.want)
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
		{name: "bad percent right after an escape", in: "/%41%zz", msg: escape},
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
		{name: "WordPress with a parameter of analytics", from: wordpressRedirect.From, target: "/?p=123&utm_source=x", want: true},
		{name: "WordPress and another post", from: wordpressRedirect.From, target: "/?p=1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redirectMatches(tt.from, tt.target); got != tt.want {
				t.Errorf("redirectMatches(%q, %q) = %t, want %t", tt.from, tt.target, got, tt.want)
			}
			if got := (Redirect{From: tt.from}).Matches(tt.target); got != tt.want {
				t.Errorf("Matches(%q) of a redirect from %q = %t, want %t", tt.target, tt.from, got, tt.want)
			}
		})
	}
}

func TestSitePath(t *testing.T) {
	const (
		site = "https://example.com"
		blog = "https://example.com/blog"
	)
	tests := []struct {
		name string
		site string
		to   string
		want string // "" if to is not in the site
	}{
		{name: "path", site: site, to: "https://example.com/about/", want: "/about/"},
		{name: "root", site: site, to: "https://example.com/", want: "/"},
		{name: "without a path", site: site, to: "https://example.com", want: "/"},
		{name: "query", site: site, to: "https://example.com/?p=123", want: "/?p=123"},
		{name: "query without a path", site: site, to: "https://example.com?p=123", want: "/?p=123"},
		{name: "query left as it is", site: site, to: "https://example.com/?q=%e4%bc%9a&a", want: "/?q=%e4%bc%9a&a"},
		// "'" is kept in paths, unlike in queries.
		{name: "apostrophe", site: site, to: "https://example.com/it's/", want: "/it's/"},
		{name: "empty query", site: site, to: "https://example.com/about/?", want: "/about/"},
		{name: "fragment", site: site, to: "https://example.com/about/?a=1#team", want: "/about/?a=1"},
		{name: "scheme in upper case", site: site, to: "HTTPS://example.com/about/", want: "/about/"},
		{name: "host in upper case", site: site, to: "https://EXAMPLE.COM/about/", want: "/about/"},
		{name: "default port", site: site, to: "https://example.com:443/about/", want: "/about/"},
		{name: "default port with a leading zero", site: site, to: "https://example.com:0443/about/", want: "/about/"},
		{name: "empty port", site: site, to: "https://example.com:/about/", want: "/about/"},
		{name: "default port of http", site: "http://example.com", to: "http://example.com:80/about/", want: "/about/"},
		{name: "port", site: "http://localhost:8080", to: "http://localhost:8080/about/", want: "/about/"},
		{name: "port with a leading zero", site: "http://localhost:8080", to: "http://localhost:08080/about/", want: "/about/"},
		{name: "IPv6", site: "https://[::1]:8443", to: "https://[::1]:8443/about/", want: "/about/"},
		{name: "Japanese", site: site, to: "https://example.com/会社/", want: "/会社/"},
		{name: "encoded Japanese", site: site, to: "https://example.com/%E4%BC%9A%E7%A4%BE/", want: "/会社/"},
		{name: "lower case hex", site: site, to: "https://example.com/%e4%bc%9a%e7%a4%be/", want: "/会社/"},
		{name: "encoded slash", site: site, to: "https://example.com/a%2Fb/", want: "/a%2Fb/"},
		{name: "encoded slash after Japanese", site: site, to: "https://example.com/会社/a%2Fb/", want: "/会社/a%2Fb/"},
		{name: "encoded percent", site: site, to: "https://example.com/100%25/", want: "/100%25/"},
		{name: "space", site: site, to: "https://example.com/a b/", want: "/a%20b/"},
		{name: "in a site at a path", site: blog, to: "https://example.com/blog/about/", want: "/about/"},
		{name: "path of the site", site: blog, to: "https://example.com/blog", want: "/"},
		{name: "path of the site with a trailing slash", site: blog, to: "https://example.com/blog/", want: "/"},
		{name: "path of the site with a query", site: blog, to: "https://example.com/blog?p=1", want: "/?p=1"},
		{name: "encoded path of the site", site: blog, to: "https://example.com/%62log/about/", want: "/about/"},
		{name: "site at a path of two segments", site: "https://example.com/a/b", to: "https://example.com/a/b/c/", want: "/c/"},
		{
			name: "site at a Japanese path",
			site: "https://example.com/ブログ",
			to:   "https://example.com/%E3%83%96%E3%83%AD%E3%82%B0/about/",
			want: "/about/",
		},
		{
			name: "site at an encoded Japanese path",
			site: "https://example.com/%E3%83%96%E3%83%AD%E3%82%B0",
			to:   "https://example.com/ブログ/about/",
			want: "/about/",
		},
		{
			name: "site at a Japanese path in lower case hex",
			site: "https://example.com/ブログ",
			to:   "https://example.com/%e3%83%96%e3%83%ad%e3%82%b0/about/",
			want: "/about/",
		},

		{name: "another scheme", site: site, to: "http://example.com/about/"},
		{name: "another host", site: site, to: "https://www.example.com/about/"},
		{name: "another port", site: site, to: "https://example.com:8443/about/"},
		{name: "default port for a site with a port", site: "http://localhost:8080", to: "http://localhost/about/"},
		{name: "outside the path of the site", site: blog, to: "https://example.com/about/"},
		{name: "root outside the path of the site", site: blog, to: "https://example.com/"},
		{name: "path that starts with the path of the site", site: blog, to: "https://example.com/blogger/about/"},
		{name: "path of the site in another case", site: blog, to: "https://example.com/Blog/about/"},
		{name: "encoded slash after the path of the site", site: blog, to: "https://example.com/blog%2Fabout/"},
		{name: "encoded slash after the Japanese path of the site", site: "https://example.com/ブログ", to: "https://example.com/ブログ%2Fabout/"},
		{name: "path instead of a URL", site: site, to: "/about/"},
		{name: "invalid URL", site: site, to: "https://example.com/%zz/"},
		{name: "empty site URL", site: "", to: "https://example.com/about/"},
		{name: "invalid site URL", site: "https://example.com/", to: "https://example.com/about/"},
		{name: "site URL in upper case", site: "https://EXAMPLE.com", to: "https://EXAMPLE.com/about/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sitePath(tt.site, tt.to)
			if got != tt.want || ok != (tt.want != "") {
				t.Errorf("sitePath(%q, %q) = %q, %t, want %q, %t", tt.site, tt.to, got, ok, tt.want, tt.want != "")
			}
			if got, ok := SitePath(tt.site, tt.to); got != tt.want || ok != (tt.want != "") {
				t.Errorf("SitePath(%q, %q) = %q, %t, want %q, %t", tt.site, tt.to, got, ok, tt.want, tt.want != "")
			}
		})
	}
}

// FuzzNormalizeRedirectFrom checks that the canonical form does not change when normalized again,
// that checkRedirectFrom does not ask to normalize it, and that a valid from matches the request it came from.
func FuzzNormalizeRedirectFrom(f *testing.F) {
	for _, s := range []string{
		"/?p=123", "/%e4%bc%9a%e7%a4%be/", "/a b/?q=it's&a=1&&", "/%%34%31", "/a/.%2e/b", "/cafe" + acute + "/", "/%FF%E4%BC", " /x?#y\t",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := normalizeRedirectFrom(s)
		if again := normalizeRedirectFrom(n); again != n {
			t.Fatalf("normalizeRedirectFrom(%q) = %q, and again %q", s, n, again)
		}
		err := checkRedirectFrom(n)
		if err != nil && strings.Contains(err.Error(), "normalize it first") {
			t.Fatalf("checkRedirectFrom(%q) = %v for the canonical form of %q", n, err, s)
		}
		if err == nil && !redirectMatches(n, s) {
			t.Fatalf("redirectMatches(%q, %q) = false", n, s)
		}
	})
}

// FuzzSitePath checks that the URL of a valid from in a valid site, the URL of the site followed by from,
// is a path in the site that from matches, so that a redirect from from to it is reported.
func FuzzSitePath(f *testing.F) {
	for _, site := range []string{"https://example.com", "https://example.com/blog", "http://localhost:8080/ブログ"} {
		for _, from := range []string{"/", "/about/?ref=top", "/会社/", "/a%2Fb/%25", "/cafe" + acute + "/", "/a:b@c/?q=?&r=/", "/%E2%80%AE"} {
			f.Add(site, from)
		}
	}
	f.Fuzz(func(t *testing.T, site, from string) {
		if checkSiteURL(site) != nil || checkRedirectFrom(from) != nil || checkHref(site+from) != nil {
			t.Skip()
		}
		if p, ok := sitePath(site, site+from); !ok || !redirectMatches(from, p) {
			t.Fatalf("sitePath(%q, %q) = %q, %t, which %q does not match", site, site+from, p, ok, from)
		}
	})
}
