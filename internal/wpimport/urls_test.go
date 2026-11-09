package wpimport

import (
	"net/url"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

func lookupURL(t *testing.T, x *urlIndex, s string) (int64, string, bool) {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", s, err)
	}
	return x.lookup(u)
}

func TestURLIndex(t *testing.T) {
	export := &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: append(linkExport().Items,
			wxr.Item{ID: 21, Type: "page", Title: "Home", Link: "https://example.com/"},
			wxr.Item{ID: 22, Type: "post", Title: "日記", Link: "https://example.com/2026/10/%E6%97%A5%E8%A8%98/"},
			wxr.Item{ID: 23, Type: "page", Title: "Same path", Link: "https://example.com/about/"},
			wxr.Item{ID: 24, Type: "post", Title: "Other host", Link: "https://other.example.org/x/"},
			wxr.Item{ID: 25, Type: "post", Title: "With a query", Link: "https://example.com/y/?z=1"},
			wxr.Item{ID: 26, Type: "attachment", Title: "No metadata", Link: "https://example.com/n/",
				AttachmentURL: "https://example.com/wp-content/uploads/2026/11/n.png",
				Meta:          wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/11/n.png"}, {Key: "_wp_attachment_metadata", Value: "a:1:{broken"}}},
			wxr.Item{ID: 27, Type: "attachment", Title: "Other place", Link: "https://example.com/o/",
				AttachmentURL: "https://example.com/files/o.png",
				Meta: wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/11/o.png"}, {Key: "_wp_attachment_metadata",
					Value: phpArray(phpString("sizes"), phpArray(phpString("small"), phpArray(phpString("file"), phpString("o-100x100.png"))))}}},
			wxr.Item{ID: 28, Type: "attachment", Title: "Names", Link: "https://example.com/m/",
				AttachmentURL: "https://example.com/wp-content/uploads/2026/11/m.png",
				Meta: wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/11/m.png"}, {Key: "_wp_attachment_metadata",
					Value: phpArray(phpString("sizes"), phpArray(
						phpString("a"), phpArray(phpString("file"), phpString("../x.png")),
						phpString("b"), phpArray(phpString("file"), phpString("a/b.png")),
						phpString("c"), phpArray(phpString("file"), phpString("")),
						phpString("d"), phpArray(phpString("width"), phpInt(1)),
						phpString("e"), phpString("not an array"),
						phpString("f"), phpArray(phpString("file"), phpString("m 1.png")),
					), phpString("original_image"), phpString(".."))}}},
			// The WXR of a site whose home option or uploads URL ends with a slash writes more than one slash in the links.
			wxr.Item{ID: 29, Type: "post", Title: "Slashes", Link: "https://example.com//2026//11/slashes//"},
			wxr.Item{ID: 30, Type: "attachment", Title: "Slashes file", Link: "https://example.com/s/",
				AttachmentURL: "https://example.com/wp-content/uploads//2026/11/s.png",
				Meta: wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/11/s.png"}, {Key: "_wp_attachment_metadata",
					Value: phpArray(phpString("sizes"), phpArray(phpString("small"), phpArray(phpString("file"), phpString("s-100x100.png"))))}}},
		),
	}
	x := newURLIndex(export)
	for _, tt := range []struct {
		url  string
		id   int64
		path string
		same bool
	}{
		// Pages.
		{"https://example.com/about/", 12, "/about/", true},
		{"https://example.com/about", 12, "/about", true},
		{"https://example.com/about/?utm=x#top", 12, "/about/", true},
		{"https://example.com/2026/10/hello/", 11, "/2026/10/hello/", true},
		{"https://example.com/2026/10/hello", 11, "/2026/10/hello", true},
		{"https://example.com/2026/10/%E6%97%A5%E8%A8%98/", 22, "/2026/10/日記/", true},
		{"https://example.com/2026/10/日記/", 22, "/2026/10/日記/", true},
		{"https://example.com/", 21, "/", true},
		{"https://example.com", 21, "/", true},
		{"https://example.com/unknown/", 0, "/unknown/", true},
		{"https://example.com/index.php", 0, "/index.php", true},
		// The scheme, the host and the port.
		{"http://example.com/about/", 12, "/about/", true},
		{"https://EXAMPLE.com/about/", 12, "/about/", true},
		{"https://example.com:443/about/", 12, "/about/", true},
		{"http://example.com:80/about/", 12, "/about/", true},
		{"https://example.com:8080/about/", 0, "", false},
		{"https://www.example.com/about/", 0, "", false},
		{"https://other.example.org/x/", 0, "", false},
		{"https://user@example.com/about/", 0, "", false},
		{"ftp://example.com/about/", 0, "", false},
		{"mailto:info@example.com", 0, "", false},
		// The links with a query do not give a path.
		{"https://example.com/y/", 0, "/y/", true},
		// The plain permalinks.
		{"https://example.com/?p=11", 11, "/", true},
		{"https://example.com/?page_id=12", 12, "/", true},
		{"https://example.com/?attachment_id=13", 13, "/", true},
		{"https://example.com/index.php?p=11", 11, "/index.php", true},
		{"https://example.com/index.php?page_id=12&x=1", 12, "/index.php", true},
		{"https://example.com/?x=1&p=11", 11, "/", true},
		{"https://example.com/?p=11&page_id=12", 11, "/", true},
		{"https://example.com/?page_id=12&p=11", 12, "/", true},
		{"https://example.com/?p=99", 99, "/", true},
		{"https://example.com/about/?p=11", 12, "/about/", true},
		// The other queries of the home are not the front page: they are pages that WordPress makes.
		{"https://example.com/?", 21, "/", true},
		{"https://example.com/?p=0", 0, "/", true},
		{"https://example.com/?p=-1", 0, "/", true},
		{"https://example.com/?p=abc", 0, "/", true},
		{"https://example.com/?p=1x", 0, "/", true},
		{"https://example.com/?p=", 0, "/", true},
		{"https://example.com/?p=99999999999999999999", 0, "/", true},
		{"https://example.com/?P=11", 0, "/", true},
		{"https://example.com/?cat=3", 0, "/", true},
		{"https://example.com/?s=tea", 0, "/", true},
		{"https://example.com/?tag=x&author=2", 0, "/", true},
		{"https://example.com/?m=202610&feed=rss2", 0, "/", true},
		{"https://example.com/index.php?cat=3", 0, "/index.php", true},
		{"https://example.com/index.php?", 0, "/index.php", true},
		// More than one slash is one, as the servers read it.
		{"https://example.com//about/", 12, "/about/", true},
		{"https://example.com/about//", 12, "/about/", true},
		{"https://example.com//", 21, "/", true},
		{"https://example.com///index.php?p=11", 11, "/index.php", true},
		{"https://example.com//2026///10/%E6%97%A5%E8%A8%98//", 22, "/2026/10/日記/", true},
		{"https://example.com//wp-content//uploads//2026/10/photo-scaled.jpg", 13, "/wp-content/uploads/2026/10/photo-scaled.jpg", true},
		{"https://example.com//unknown//", 0, "/unknown/", true},
		// The paths that the WXR writes with more than one slash are the same paths, whether the address has one or more.
		{"https://example.com//2026//11/slashes//", 29, "/2026/11/slashes/", true},
		{"https://example.com/2026/11/slashes/", 29, "/2026/11/slashes/", true},
		{"https://example.com/2026/11/slashes", 29, "/2026/11/slashes", true},
		{"https://example.com/wp-content/uploads//2026/11/s.png", 30, "/wp-content/uploads/2026/11/s.png", true},
		{"https://example.com/wp-content/uploads/2026/11/s.png", 30, "/wp-content/uploads/2026/11/s.png", true},
		{"https://example.com/wp-content/uploads//2026/11/s-100x100.png", 30, "/wp-content/uploads/2026/11/s-100x100.png", true},
		{"https://example.com/wp-content/uploads/2026/11/s-100x100.png", 30, "/wp-content/uploads/2026/11/s-100x100.png", true},
		// The files.
		{"https://example.com/wp-content/uploads/2026/10/photo-scaled.jpg", 13, "/wp-content/uploads/2026/10/photo-scaled.jpg", true},
		{"https://example.com/wp-content/uploads/2026/10/photo-300x200-scaled.jpg", 13, "/wp-content/uploads/2026/10/photo-300x200-scaled.jpg", true},
		{"https://example.com/wp-content/uploads/2026/10/photo-150x150.jpg", 13, "/wp-content/uploads/2026/10/photo-150x150.jpg", true},
		{"https://example.com/wp-content/uploads/2026/10/photo.jpg", 13, "/wp-content/uploads/2026/10/photo.jpg", true},
		{"https://example.com/wp-content/uploads/2026/10/photo-300x200.jpg", 0, "/wp-content/uploads/2026/10/photo-300x200.jpg", true},
		{"https://example.com/photo/", 13, "/photo/", true},
		{"https://example.com/wp-content/uploads/2026/10/報告.pdf", 14, "/wp-content/uploads/2026/10/報告.pdf", true},
		{"https://example.com/wp-content/uploads/2026/10/%E5%A0%B1%E5%91%8A.pdf", 14, "/wp-content/uploads/2026/10/報告.pdf", true},
		// Metadata that cannot be read, and files in another place.
		{"https://example.com/wp-content/uploads/2026/11/n.png", 26, "/wp-content/uploads/2026/11/n.png", true},
		{"https://example.com/files/o.png", 27, "/files/o.png", true},
		{"https://example.com/files/o-100x100.png", 0, "/files/o-100x100.png", true},
		{"https://example.com/wp-content/uploads/2026/11/m.png", 28, "/wp-content/uploads/2026/11/m.png", true},
		{"https://example.com/wp-content/uploads/2026/11/m%201.png", 28, "/wp-content/uploads/2026/11/m%201.png", true},
		{"https://example.com/wp-content/uploads/2026/x.png", 0, "/wp-content/uploads/2026/x.png", true},
		{"https://example.com/wp-content/uploads/2026/11/a/b.png", 0, "/wp-content/uploads/2026/11/a/b.png", true},
	} {
		id, path, same := lookupURL(t, x, tt.url)
		if id != tt.id || path != tt.path || same != tt.same {
			t.Errorf("lookup(%q) = %d, %q, %t, want %d, %q, %t", tt.url, id, path, same, tt.id, tt.path, tt.same)
		}
	}
}

func TestURLIndexHomeWithAPath(t *testing.T) {
	x := newURLIndex(&wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com/blog/"},
		Items: []wxr.Item{
			{ID: 1, Type: "post", Link: "https://example.com/blog/2026/hello/"},
			{ID: 2, Type: "page", Link: "https://example.com/blog/"},
			{ID: 3, Type: "post", Link: "https://example.com/2026/outside/"},
			{ID: 4, Type: "attachment", Link: "https://example.com/blog/a/", AttachmentURL: "https://example.com/blog/wp-content/uploads/2026/a.png",
				Meta: wxr.MetaList{{Key: "_wp_attached_file", Value: "2026/a.png"}}},
		},
	})
	for _, tt := range []struct {
		url  string
		id   int64
		path string
		same bool
	}{
		{"https://example.com/blog/2026/hello/", 1, "/2026/hello/", true},
		{"https://example.com/blog/", 2, "/", true},
		{"https://example.com/blog", 2, "/", true},
		{"https://example.com/blog/?p=1", 1, "/", true},
		{"https://example.com/blog/index.php?page_id=2", 2, "/index.php", true},
		{"https://example.com/blog/wp-content/uploads/2026/a.png", 4, "/wp-content/uploads/2026/a.png", true},
		{"https://example.com/2026/outside/", 0, "", false},
		{"https://example.com/", 0, "", false},
		{"https://example.com/blogger/", 0, "", false},
		{"https://example.com/blog2/", 0, "", false},
	} {
		id, path, same := lookupURL(t, x, tt.url)
		if id != tt.id || path != tt.path || same != tt.same {
			t.Errorf("lookup(%q) = %d, %q, %t, want %d, %q, %t", tt.url, id, path, same, tt.id, tt.path, tt.same)
		}
	}
	// The path of the home is made once, in the form of the paths of the index.
	if x.base != "/blog" {
		t.Errorf("the path of the home is %q, want /blog", x.base)
	}
}

func TestURLIndexPathLength(t *testing.T) {
	// A path of more than permPathBytes, as a link of an item may not have, is not the path of an address in the site either.
	x := newURLIndex(&wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}})
	for _, tt := range []struct {
		n    int
		same bool
	}{{permPathBytes - 1, true}, {permPathBytes, false}} {
		if _, _, same := lookupURL(t, x, "https://example.com/"+strings.Repeat("a", tt.n)); same != tt.same {
			t.Errorf("a path of %d bytes is in the site = %t, want %t", tt.n+1, same, tt.same)
		}
	}
}

func TestURLIndexWithoutAHome(t *testing.T) {
	for _, e := range []*wxr.Export{nil, {}, {Channel: wxr.Channel{Link: "not a link"}}, {Channel: wxr.Channel{Link: "ftp://example.com/"}}} {
		x := newURLIndex(e)
		if id, path, same := lookupURL(t, x, "https://example.com/about/"); id != 0 || path != "" || same {
			t.Errorf("lookup = %d, %q, %t, want nothing", id, path, same)
		}
	}
	// The address of the blog is the home if the channel has no link.
	x := newURLIndex(&wxr.Export{Channel: wxr.Channel{BaseBlogURL: "https://example.com"}, Items: []wxr.Item{{ID: 1, Link: "https://example.com/a/"}}})
	if id, _, same := lookupURL(t, x, "https://example.com/a/"); id != 1 || !same {
		t.Errorf("lookup = %d, %t, want 1, true", id, same)
	}
}

func TestURLIndexIsMadeOnce(t *testing.T) {
	imp := New("site.xml", linkExport(), NewReport(Info{}))
	if imp.urls != nil {
		t.Fatal("the index is made before it is needed")
	}
	if x := imp.urlIndex(); x == nil || imp.urlIndex() != x {
		t.Error("urlIndex does not return the same index")
	}
}

func TestURLIndexFirstPostHasThePath(t *testing.T) {
	x := newURLIndex(&wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			{ID: 7, Type: "post", Link: "https://example.com/a/"},
			{ID: 3, Type: "page", Link: "https://example.com/a/"},
			{ID: 9, Type: "attachment", Link: "https://example.com/a/", AttachmentURL: "https://example.com/a/"},
		},
	})
	if id, _, _ := lookupURL(t, x, "https://example.com/a/"); id != 7 {
		t.Errorf("lookup = %d, want 7, the first in the WXR", id)
	}
}
