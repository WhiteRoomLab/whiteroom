package build

import (
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// testRedirect is a redirect that a test adds to the fixture.
type testRedirect struct {
	from, to string
	status   int
}

// Redirects of the tests of the server files, besides that of the fixture from /?p=123 to the post of 6.11.9.
var (
	// basicRedirects have queries, which match in any order, and the one with more parameters first; a from in
	// Japanese; an absolute target with "$"; a from that nginx and Apache cannot match; and a query at the path of a
	// page.
	basicRedirects = []testRedirect{
		{"/?a=1&b=2", "/about/", 301},
		{"/旧ページ/", servicesID, 302},
		{"/partner/", "https://example.org/パートナー/?a=1&b=$", 302},
		{"/a%2Fb/", "/about/", 301},
		{"/news/?r=1", "/r/", 302},
		{"/?a=1", "/contact/", 301},
	}
	// escapingRedirects have what the files must escape or encode: values in Japanese, "$" in froms and targets, a
	// target with a query and a fragment, a path with a space, `"`, "\" before "n" (a line break in the strings of
	// nginx), "?", "#" and "%", a parameter without a value, an encoded "+", and a ".".
	escapingRedirects = []testRedirect{
		{"/?q=新", "/news/", 301},
		{"/?x=$y", "/contact/?from=old#form", 301},
		{"/$var/", "/target/$1/", 301},
		{"/a%20b%22c%5Cn%3Fe%23f%25/", "/privacy/", 302},
		{"/?e", "/e/", 301},
		{"/?k=a%2Bb", "/k/", 301},
		{"/old.html", "/about/", 301},
	}
	// leftOutRedirects are left out of nginx.conf and .htaccess: "/a+b/" after "/a%2Bb/", which nginx and Apache
	// decode alike, a control character, an empty segment, the path of .htaccess, and bytes that are not UTF-8.
	leftOutRedirects = []testRedirect{
		{"/a%2Bb/", "/services/", 301},
		{"/a+b/", "/privacy/", 301},
		{"/a%01b/", "/about/", 301},
		{"/a//b/", "/about/", 301},
		{"/.htaccess?x=1", "/about/", 301},
		{"/%FF/", "/about/", 301},
	}
)

// serverSource returns the fixture at siteURL, with prefixDefaultLanguage if prefix, and the redirects of sets, which
// have the IDs newID("rdr", n) from 1 in order.
func serverSource(t testing.TB, siteURL string, prefix bool, sets ...[]testRedirect) *memSource {
	t.Helper()
	src := testSource(t)
	site := testSite()
	site.URL, site.PrefixDefaultLanguage = siteURL, prefix
	src.putSettings(t, site)
	n := 0
	for _, set := range sets {
		for _, x := range set {
			n++
			id := newID("rdr", n)
			src.put(t, content.RefRedirect, id, content.Redirect{ID: id, From: x.from, To: x.to, Status: x.status})
		}
	}
	return src
}

// mustWriteServerFiles writes the server files of b into a new directory, and returns it.
func mustWriteServerFiles(t *testing.T, b *builder) string {
	t.Helper()
	dir, rel := newPublic(t)
	if err := b.writeServerFiles(rel); err != nil {
		t.Fatalf("writeServerFiles() = %v", err)
	}
	return dir
}

// checkServerFiles checks the files that mustWriteServerFiles writes for b, which must be the same each time, and the
// warnings of b.
func checkServerFiles(t *testing.T, b *builder, list, nginx, htaccess string, warnings []string) {
	t.Helper()
	dir := mustWriteServerFiles(t, b)
	for _, f := range []struct{ name, want string }{
		{"server.json", list},
		{"nginx.conf", nginx},
		{"public/.htaccess", htaccess},
	} {
		if got := readFile(t, dir, f.name); got != f.want {
			t.Errorf("%s:\n%s\nwant:\n%s", f.name, got, f.want)
		}
	}
	if got := listFiles(t, dir); !slices.Equal(got, []string{"nginx.conf", "public/.htaccess", "server.json"}) {
		t.Errorf("files %q", got)
	}
	if got := warningTexts(b); !slices.Equal(got, warnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(warnings, "\n"))
	}
	again := mustWriteServerFiles(t, b)
	for _, name := range []string{"server.json", "nginx.conf", "public/.htaccess"} {
		if readFile(t, again, name) != readFile(t, dir, name) {
			t.Errorf("%s differs when written again", name)
		}
	}
}

// leftOut returns the warning about a redirect that is left out of nginx.conf and .htaccess, in the file of the
// redirect newID("rdr", n), at its from, with the reason.
func leftOut(n int, reason string) string {
	return fileOf(content.RefRedirect, newID("rdr", n)) + ":4:3: /from: " + reason +
		"; the redirect is left out of nginx.conf and .htaccess, so only the built-in server serves it"
}

// TestServerFiles checks server.json, nginx.conf and .htaccess of a site at the root of its host, and of one under a
// path with prefixDefaultLanguage.
func TestServerFiles(t *testing.T) {
	encodedSlash := leftOut(4, `the path has an encoded "/" (%2F)`)
	t.Run("root", func(t *testing.T) {
		b := mustLoad(t, serverSource(t, "https://example.com", false, basicRedirects))
		mustResolve(t, b)
		checkServerFiles(t, b, `{
  "base": "",
  "redirects": [
    {
      "from": "/?a=1",
      "to": "/contact/",
      "status": 301
    },
    {
      "from": "/?a=1&b=2",
      "to": "/about/",
      "status": 301
    },
    {
      "from": "/?p=123",
      "to": "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/",
      "status": 301
    },
    {
      "from": "/a%2Fb/",
      "to": "/about/",
      "status": 301
    },
    {
      "from": "/home/",
      "to": "/",
      "status": 301
    },
    {
      "from": "/news/?r=1",
      "to": "/r/",
      "status": 302
    },
    {
      "from": "/partner/",
      "to": "https://example.org/%E3%83%91%E3%83%BC%E3%83%88%E3%83%8A%E3%83%BC/?a=1&b=$",
      "status": 302
    },
    {
      "from": "/旧ページ/",
      "to": "/services/",
      "status": 302
    }
  ],
  "notFound": [
    {
      "prefix": "/en/",
      "page": "/en/404.html"
    },
    {
      "prefix": "/",
      "page": "/404.html"
    }
  ],
  "index": [
    "index.html",
    "index.xml"
  ]
}
`, `index index.html index.xml;
set $whiteroom_not_found "/404.html";
if ($uri ~ "^/en/") {
    set $whiteroom_not_found "/en/404.html";
}
error_page 404 $whiteroom_not_found;
location = "/.htaccess" {
    return 404;
}
location = "/" {
    if ($args ~ "^(?=(?:.*&)?a=1(?:&|$))(?=(?:.*&)?b=2(?:&|$))") {
        return 301 "/about/";
    }
    if ($args ~ "^(?=(?:.*&)?a=1(?:&|$))") {
        return 301 "/contact/";
    }
    if ($args ~ "^(?=(?:.*&)?p=123(?:&|$))") {
        return 301 "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/";
    }
}
location = "/home" {
    return 301 "/";
}
location = "/home/" {
    return 301 "/";
}
location = "/news" {
    if ($args ~ "^(?=(?:.*&)?r=1(?:&|$))") {
        return 302 "/r/";
    }
}
location = "/news/" {
    if ($args ~ "^(?=(?:.*&)?r=1(?:&|$))") {
        return 302 "/r/";
    }
}
location = "/partner" {
    return 302 "https://example.org/%E3%83%91%E3%83%BC%E3%83%88%E3%83%8A%E3%83%BC/?a=1&b=%24";
}
location = "/partner/" {
    return 302 "https://example.org/%E3%83%91%E3%83%BC%E3%83%88%E3%83%8A%E3%83%BC/?a=1&b=%24";
}
location = "/旧ページ" {
    return 302 "/services/";
}
location = "/旧ページ/" {
    return 302 "/services/";
}
`, `DirectoryIndex index.html index.xml
ErrorDocument 404 /404.html
<If "%{REQUEST_URI} =~ m#^/en/#">
    ErrorDocument 404 /en/404.html
</If>
RewriteEngine On
RewriteCond %{QUERY_STRING} (?:^|&)a=1(?:&|$)
RewriteCond %{QUERY_STRING} (?:^|&)b=2(?:&|$)
RewriteRule ^$ /about/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)a=1(?:&|$)
RewriteRule ^$ /contact/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)p=123(?:&|$)
RewriteRule ^$ /2026/10/26/\%E6\%96\%B0\%E3\%82\%B5\%E3\%83\%BC\%E3\%83\%93\%E3\%82\%B9/ [R=301,L,QSD,NE]
RewriteRule ^home/?$ / [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)r=1(?:&|$)
RewriteRule ^news/?$ /r/ [R=302,L,QSD,NE]
RewriteRule ^partner/?$ https://example.org/\%E3\%83\%91\%E3\%83\%BC\%E3\%83\%88\%E3\%83\%8A\%E3\%83\%BC/?a=1&b=\$ [R=302,L,QSD,NE]
RewriteRule ^\xE6\x97\xA7\xE3\x83\x9A\xE3\x83\xBC\xE3\x82\xB8/?$ /services/ [R=302,L,QSD,NE]
`, []string{encodedSlash})
	})

	t.Run("path with prefixDefaultLanguage", func(t *testing.T) {
		b := mustLoad(t, serverSource(t, "https://example.com/blog", true, basicRedirects))
		mustResolve(t, b)
		checkServerFiles(t, b, `{
  "base": "/blog",
  "redirects": [
    {
      "from": "/",
      "to": "/blog/ja/",
      "status": 302
    },
    {
      "from": "/?a=1",
      "to": "/blog/contact/",
      "status": 301
    },
    {
      "from": "/?a=1&b=2",
      "to": "/blog/about/",
      "status": 301
    },
    {
      "from": "/?p=123",
      "to": "/blog/ja/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/",
      "status": 301
    },
    {
      "from": "/a%2Fb/",
      "to": "/blog/about/",
      "status": 301
    },
    {
      "from": "/ja/home/",
      "to": "/blog/ja/",
      "status": 301
    },
    {
      "from": "/news/?r=1",
      "to": "/blog/r/",
      "status": 302
    },
    {
      "from": "/partner/",
      "to": "https://example.org/%E3%83%91%E3%83%BC%E3%83%88%E3%83%8A%E3%83%BC/?a=1&b=$",
      "status": 302
    },
    {
      "from": "/旧ページ/",
      "to": "/blog/ja/services/",
      "status": 302
    }
  ],
  "notFound": [
    {
      "prefix": "/ja/",
      "page": "/ja/404.html"
    },
    {
      "prefix": "/en/",
      "page": "/en/404.html"
    },
    {
      "prefix": "/",
      "page": "/ja/404.html"
    }
  ],
  "index": [
    "index.html",
    "index.xml"
  ]
}
`, `index index.html index.xml;
set $whiteroom_not_found "/blog/ja/404.html";
if ($uri ~ "^/blog/en/") {
    set $whiteroom_not_found "/blog/en/404.html";
}
error_page 404 $whiteroom_not_found;
location = "/blog/.htaccess" {
    return 404;
}
location = "/blog/" {
    if ($args ~ "^(?=(?:.*&)?a=1(?:&|$))(?=(?:.*&)?b=2(?:&|$))") {
        return 301 "/blog/about/";
    }
    if ($args ~ "^(?=(?:.*&)?a=1(?:&|$))") {
        return 301 "/blog/contact/";
    }
    if ($args ~ "^(?=(?:.*&)?p=123(?:&|$))") {
        return 301 "/blog/ja/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/";
    }
    return 302 "/blog/ja/";
}
location = "/blog/ja/home" {
    return 301 "/blog/ja/";
}
location = "/blog/ja/home/" {
    return 301 "/blog/ja/";
}
location = "/blog/news" {
    if ($args ~ "^(?=(?:.*&)?r=1(?:&|$))") {
        return 302 "/blog/r/";
    }
}
location = "/blog/news/" {
    if ($args ~ "^(?=(?:.*&)?r=1(?:&|$))") {
        return 302 "/blog/r/";
    }
}
location = "/blog/partner" {
    return 302 "https://example.org/%E3%83%91%E3%83%BC%E3%83%88%E3%83%8A%E3%83%BC/?a=1&b=%24";
}
location = "/blog/partner/" {
    return 302 "https://example.org/%E3%83%91%E3%83%BC%E3%83%88%E3%83%8A%E3%83%BC/?a=1&b=%24";
}
location = "/blog/旧ページ" {
    return 302 "/blog/ja/services/";
}
location = "/blog/旧ページ/" {
    return 302 "/blog/ja/services/";
}
`, `DirectoryIndex index.html index.xml
ErrorDocument 404 /blog/ja/404.html
<If "%{REQUEST_URI} =~ m#^/blog/en/#">
    ErrorDocument 404 /blog/en/404.html
</If>
RewriteEngine On
RewriteCond %{QUERY_STRING} (?:^|&)a=1(?:&|$)
RewriteCond %{QUERY_STRING} (?:^|&)b=2(?:&|$)
RewriteRule ^$ /blog/about/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)a=1(?:&|$)
RewriteRule ^$ /blog/contact/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)p=123(?:&|$)
RewriteRule ^$ /blog/ja/2026/10/26/\%E6\%96\%B0\%E3\%82\%B5\%E3\%83\%BC\%E3\%83\%93\%E3\%82\%B9/ [R=301,L,QSD,NE]
RewriteRule ^$ /blog/ja/ [R=302,L,QSD,NE]
RewriteRule ^ja/home/?$ /blog/ja/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)r=1(?:&|$)
RewriteRule ^news/?$ /blog/r/ [R=302,L,QSD,NE]
RewriteRule ^partner/?$ https://example.org/\%E3\%83\%91\%E3\%83\%BC\%E3\%83\%88\%E3\%83\%8A\%E3\%83\%BC/?a=1&b=\$ [R=302,L,QSD,NE]
RewriteRule ^\xE6\x97\xA7\xE3\x83\x9A\xE3\x83\xBC\xE3\x82\xB8/?$ /blog/ja/services/ [R=302,L,QSD,NE]
`, []string{encodedSlash})
	})
}

// TestServerEscaping checks what the server files escape and encode, in a site under a path in Japanese with a space,
// which nginx.conf has decoded and .htaccess percent-encoded.
func TestServerEscaping(t *testing.T) {
	b := mustLoad(t, serverSource(t, "https://example.com/ブ%20ログ", false, escapingRedirects))
	mustResolve(t, b)
	checkServerFiles(t, b, `{
  "base": "/ブ%20ログ",
  "redirects": [
    {
      "from": "/$var/",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/target/$1/",
      "status": 301
    },
    {
      "from": "/?e",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/e/",
      "status": 301
    },
    {
      "from": "/?k=a%2Bb",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/k/",
      "status": 301
    },
    {
      "from": "/?p=123",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/",
      "status": 301
    },
    {
      "from": "/?q=新",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/news/",
      "status": 301
    },
    {
      "from": "/?x=$y",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/contact/?from=old#form",
      "status": 301
    },
    {
      "from": "/a%20b%22c%5Cn%3Fe%23f%25/",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/privacy/",
      "status": 302
    },
    {
      "from": "/home/",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/",
      "status": 301
    },
    {
      "from": "/old.html",
      "to": "/%E3%83%96%20%E3%83%AD%E3%82%B0/about/",
      "status": 301
    }
  ],
  "notFound": [
    {
      "prefix": "/en/",
      "page": "/en/404.html"
    },
    {
      "prefix": "/",
      "page": "/404.html"
    }
  ],
  "index": [
    "index.html",
    "index.xml"
  ]
}
`, `index index.html index.xml;
set $whiteroom_not_found "/ブ ログ/404.html";
if ($uri ~ "^/\xE3\x83\x96\x20\xE3\x83\xAD\xE3\x82\xB0/en/") {
    set $whiteroom_not_found "/ブ ログ/en/404.html";
}
error_page 404 $whiteroom_not_found;
location = "/ブ ログ/.htaccess" {
    return 404;
}
location = "/ブ ログ/" {
    if ($args ~ "^(?=(?:.*&)?e(?:&|$))") {
        return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/e/";
    }
    if ($args ~ "^(?=(?:.*&)?k=a%2Bb(?:&|$))") {
        return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/k/";
    }
    if ($args ~ "^(?=(?:.*&)?p=123(?:&|$))") {
        return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/";
    }
    if ($args ~ "^(?=(?:.*&)?q=%E6%96%B0(?:&|$))") {
        return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/news/";
    }
    if ($args ~ "^(?=(?:.*&)?x=\x24y(?:&|$))") {
        return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/contact/?from=old#form";
    }
}
location = "/ブ ログ/$var" {
    return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/target/%241/";
}
location = "/ブ ログ/$var/" {
    return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/target/%241/";
}
location = "/ブ ログ/a b\"c\\n?e#f%" {
    return 302 "/%E3%83%96%20%E3%83%AD%E3%82%B0/privacy/";
}
location = "/ブ ログ/a b\"c\\n?e#f%/" {
    return 302 "/%E3%83%96%20%E3%83%AD%E3%82%B0/privacy/";
}
location = "/ブ ログ/home" {
    return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/";
}
location = "/ブ ログ/home/" {
    return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/";
}
location = "/ブ ログ/old.html" {
    return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/about/";
}
location = "/ブ ログ/old.html/" {
    return 301 "/%E3%83%96%20%E3%83%AD%E3%82%B0/about/";
}
`, `DirectoryIndex index.html index.xml
ErrorDocument 404 /%E3%83%96%20%E3%83%AD%E3%82%B0/404.html
<If "%{REQUEST_URI} =~ m#^/\xE3\x83\x96\x20\xE3\x83\xAD\xE3\x82\xB0/en/#">
    ErrorDocument 404 /%E3%83%96%20%E3%83%AD%E3%82%B0/en/404.html
</If>
RewriteEngine On
RewriteCond %{QUERY_STRING} (?:^|&)e(?:&|$)
RewriteRule ^$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/e/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)k=a%2Bb(?:&|$)
RewriteRule ^$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/k/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)p=123(?:&|$)
RewriteRule ^$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/2026/10/26/\%E6\%96\%B0\%E3\%82\%B5\%E3\%83\%BC\%E3\%83\%93\%E3\%82\%B9/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)q=%E6%96%B0(?:&|$)
RewriteRule ^$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/news/ [R=301,L,QSD,NE]
RewriteCond %{QUERY_STRING} (?:^|&)x=\x24y(?:&|$)
RewriteRule ^$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/contact/?from=old#form [R=301,L,QSD,NE]
RewriteRule ^\x24var/?$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/target/\$1/ [R=301,L,QSD,NE]
RewriteRule ^a\x20b\x22c\x5Cn\x3Fe\x23f%/?$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/privacy/ [R=302,L,QSD,NE]
RewriteRule ^home/?$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/ [R=301,L,QSD,NE]
RewriteRule ^old\x2Ehtml/?$ /\%E3\%83\%96\%20\%E3\%83\%AD\%E3\%82\%B0/about/ [R=301,L,QSD,NE]
`, nil)
}

// TestServerLeftOut checks the redirects that nginx.conf and .htaccess leave out, which server.json has.
func TestServerLeftOut(t *testing.T) {
	b := mustLoad(t, serverSource(t, "https://example.com", false, leftOutRedirects))
	mustResolve(t, b)
	checkServerFiles(t, b, `{
  "base": "",
  "redirects": [
    {
      "from": "/%FF/",
      "to": "/about/",
      "status": 301
    },
    {
      "from": "/.htaccess?x=1",
      "to": "/about/",
      "status": 301
    },
    {
      "from": "/?p=123",
      "to": "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/",
      "status": 301
    },
    {
      "from": "/a%01b/",
      "to": "/about/",
      "status": 301
    },
    {
      "from": "/a%2Bb/",
      "to": "/services/",
      "status": 301
    },
    {
      "from": "/a+b/",
      "to": "/privacy/",
      "status": 301
    },
    {
      "from": "/a//b/",
      "to": "/about/",
      "status": 301
    },
    {
      "from": "/home/",
      "to": "/",
      "status": 301
    }
  ],
  "notFound": [
    {
      "prefix": "/en/",
      "page": "/en/404.html"
    },
    {
      "prefix": "/",
      "page": "/404.html"
    }
  ],
  "index": [
    "index.html",
    "index.xml"
  ]
}
`, `index index.html index.xml;
set $whiteroom_not_found "/404.html";
if ($uri ~ "^/en/") {
    set $whiteroom_not_found "/en/404.html";
}
error_page 404 $whiteroom_not_found;
location = "/.htaccess" {
    return 404;
}
location = "/" {
    if ($args ~ "^(?=(?:.*&)?p=123(?:&|$))") {
        return 301 "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/";
    }
}
location = "/a+b" {
    return 301 "/services/";
}
location = "/a+b/" {
    return 301 "/services/";
}
location = "/home" {
    return 301 "/";
}
location = "/home/" {
    return 301 "/";
}
`, `DirectoryIndex index.html index.xml
ErrorDocument 404 /404.html
<If "%{REQUEST_URI} =~ m#^/en/#">
    ErrorDocument 404 /en/404.html
</If>
RewriteEngine On
RewriteCond %{QUERY_STRING} (?:^|&)p=123(?:&|$)
RewriteRule ^$ /2026/10/26/\%E6\%96\%B0\%E3\%82\%B5\%E3\%83\%BC\%E3\%83\%93\%E3\%82\%B9/ [R=301,L,QSD,NE]
RewriteRule ^a\x2Bb/?$ /services/ [R=301,L,QSD,NE]
RewriteRule ^home/?$ / [R=301,L,QSD,NE]
`, []string{
		leftOut(2, `nginx and Apache decode the path as that of the from "/a%2Bb/" of `+
			fileOf(content.RefRedirect, newID("rdr", 1))),
		leftOut(3, "the path has a control character"),
		leftOut(4, "the path has an empty segment"),
		leftOut(5, "the path is that of .htaccess"),
		leftOut(6, "the path has bytes that are not UTF-8"),
	})
}

// TestServerBase checks that the files for nginx and Apache have only the directory index if the path of the site URL
// cannot be written for them, with a warning at the URL.
func TestServerBase(t *testing.T) {
	for _, tt := range []struct {
		url, base, reason string
	}{
		{"https://example.com/a$b", "/a$b", `"$", which nginx reads as a variable`},
		{"https://example.com/a%3Fb", "/a%3Fb", `"?" (%3F), which nginx reads as the start of a query`},
		{"https://example.com/a%2fb", "/a%2Fb", `an encoded "/" (%2F)`},
		{"https://example.com/a%7Fb", "/a%7Fb", "a control character"},
		{"https://example.com/a%FFb", "/a%FFb", "bytes that are not UTF-8"},
	} {
		t.Run(tt.url, func(t *testing.T) {
			b := mustLoad(t, serverSource(t, tt.url, false, basicRedirects))
			mustResolve(t, b)
			dir := mustWriteServerFiles(t, b)
			if got := readFile(t, dir, "nginx.conf"); got != "index index.html index.xml;\n" {
				t.Errorf("nginx.conf:\n%s", got)
			}
			if got := readFile(t, dir, "public/.htaccess"); got != "DirectoryIndex index.html index.xml\n" {
				t.Errorf(".htaccess:\n%s", got)
			}
			list := readFile(t, dir, "server.json")
			if !strings.HasPrefix(list, "{\n  \"base\": \""+tt.base+"\",\n") || !strings.Contains(list, `"from": "/a%2Fb/"`) {
				t.Errorf("server.json:\n%s", list)
			}
			want := []string{
				"site/settings.json:10:3: /url: the path of the site URL has " + tt.reason + "; nginx.conf and .htaccess " +
					"have only the directory index, so only the built-in server serves the redirects and the 404 pages",
			}
			if got := warningTexts(b); !slices.Equal(got, want) {
				t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// TestNotFoundPages checks the 404 pages of server.json, and those of nginx.conf and .htaccess, of sites in one or
// three languages, with or without prefixDefaultLanguage, and without 404 pages.
func TestNotFoundPages(t *testing.T) {
	tests := []struct {
		name      string
		languages []string
		prefix    bool
		pages     []notFoundPage
		nginx     string
		htaccess  string
	}{
		{
			name:      "one language",
			languages: []string{"ja"},
			pages:     []notFoundPage{{"/", "/404.html"}},
			nginx:     `error_page 404 "/blog/404.html";` + "\n",
			htaccess:  "ErrorDocument 404 /blog/404.html\n",
		},
		{
			name:      "one language with prefixDefaultLanguage",
			languages: []string{"ja"},
			prefix:    true,
			pages:     []notFoundPage{{"/ja/", "/ja/404.html"}, {"/", "/ja/404.html"}},
			nginx:     `error_page 404 "/blog/ja/404.html";` + "\n",
			htaccess:  "ErrorDocument 404 /blog/ja/404.html\n",
		},
		{
			name:      "three languages with prefixDefaultLanguage",
			languages: []string{"ja", "en", "zh-Hant"},
			prefix:    true,
			pages: []notFoundPage{
				{"/zh-hant/", "/zh-hant/404.html"}, {"/ja/", "/ja/404.html"}, {"/en/", "/en/404.html"}, {"/", "/ja/404.html"},
			},
			nginx: `set $whiteroom_not_found "/blog/ja/404.html";
if ($uri ~ "^/blog/zh-hant/") {
    set $whiteroom_not_found "/blog/zh-hant/404.html";
}
if ($uri ~ "^/blog/en/") {
    set $whiteroom_not_found "/blog/en/404.html";
}
error_page 404 $whiteroom_not_found;
`,
			htaccess: `ErrorDocument 404 /blog/ja/404.html
<If "%{REQUEST_URI} =~ m#^/blog/zh-hant/#">
    ErrorDocument 404 /blog/zh-hant/404.html
</If>
<If "%{REQUEST_URI} =~ m#^/blog/en/#">
    ErrorDocument 404 /blog/en/404.html
</If>
`,
		},
		{
			name:      "no 404 pages",
			languages: []string{"ja", "en"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &builder{
				site: &content.Site{Languages: tt.languages, DefaultLanguage: "ja", PrefixDefaultLanguage: tt.prefix},
				base: "/blog",
			}
			if tt.pages != nil {
				for _, l := range tt.languages {
					b.pages = append(b.pages, &page{kind: theme.KindNotFound, language: l, path: b.site.LanguagePath(l) + "404.html"})
				}
			}
			b.pages = append(b.pages, &page{kind: theme.KindEntry, language: "ja", path: "/about/"})
			pages := b.notFoundPages()
			if !slices.Equal(pages, tt.pages) {
				t.Errorf("notFoundPages() = %q, want %q", pages, tt.pages)
			}
			c := b.serverConfig(pages)
			nginx := "index index.html index.xml;\n" + tt.nginx + "location = \"/blog/.htaccess\" {\n    return 404;\n}\n"
			if got := string(b.nginxConf(c)); got != nginx {
				t.Errorf("nginx.conf:\n%s\nwant:\n%s", got, nginx)
			}
			if got, want := string(b.htaccess(c)), "DirectoryIndex index.html index.xml\n"+tt.htaccess; got != want {
				t.Errorf(".htaccess:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestDecodePath(t *testing.T) {
	for _, tt := range []struct{ path, decoded, reason string }{
		{"", "", ""},
		{"/", "/", ""},
		{"/about/", "/about/", ""},
		{"/新サービス/", "/新サービス/", ""},
		{"/%E6%96%B0%e3%82%b5/", "/新サ/", ""},
		{"/a%20b%22c%5Cd%3Fe%23f%25+$/", `/a b"c\d?e#f%+$/`, ""},
		{"/ホーム%E3%80%80ページ/", "/ホーム" + string(rune(0x3000)) + "ページ/", ""},
		{"/a%2Fb/", "", `an encoded "/" (%2F)`},
		{"/a%2fb/", "", `an encoded "/" (%2F)`},
		{"/a%00b/", "", "a control character"},
		{"/a%09b/", "", "a control character"},
		{"/a%7Fb/", "", "a control character"},
		{"/a%C2%85b/", "", "a control character"},
		{"/a%FFb/", "", "bytes that are not UTF-8"},
		{"/%8E%9E/", "", "bytes that are not UTF-8"},
		{"/a//b/", "", "an empty segment"},
		{"/a/b//", "", "an empty segment"},
		{"/a%zz/", "", "an invalid percent-encoding"},
	} {
		decoded, reason := decodePath(tt.path)
		if decoded != tt.decoded || reason != tt.reason {
			t.Errorf("decodePath(%q) = %q, %q; want %q, %q", tt.path, decoded, reason, tt.decoded, tt.reason)
		}
	}
}

func TestBrowserParam(t *testing.T) {
	for _, tt := range []struct{ param, want string }{
		{"p=123", "p=123"},
		{"q=新", "q=%E6%96%B0"},
		{"新=サ%20ー%FF", "%E6%96%B0=%E3%82%B5%20%E3%83%BC%FF"},
		{"k=a%2Bb+c$!*'()", "k=a%2Bb+c$!*'()"},
	} {
		if got := browserParam(tt.param); got != tt.want {
			t.Errorf("browserParam(%q) = %q, want %q", tt.param, got, tt.want)
		}
	}
}

func TestQuoteRegexp(t *testing.T) {
	for _, tt := range []struct{ s, want string }{
		{"", ""},
		{"Az09-_/=%", "Az09-_/=%"},
		{`.*+?()[]{}|^$\#"' &~`, `\x2E\x2A\x2B\x3F\x28\x29\x5B\x5D\x7B\x7D\x7C\x5E\x24\x5C\x23\x22\x27\x20\x26\x7E`},
		{"新", `\xE6\x96\xB0`},
		{"\x00\n\x7f", `\x00\x0A\x7F`},
	} {
		if got := quoteRegexp(tt.s); got != tt.want {
			t.Errorf("quoteRegexp(%q) = %q, want %q", tt.s, got, tt.want)
		}
	}
}
