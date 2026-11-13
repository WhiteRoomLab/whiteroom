package wpimport

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
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

// planMedia gives the attachments with the post IDs posts the ID and the reference that the plan of the media gives, and
// returns the IDs by the post ID. The other attachments are not planned.
func planMedia(t *testing.T, imp *Import, posts ...int64) map[int64]string {
	t.Helper()
	ids := make(map[int64]string)
	for _, post := range posts {
		it := imp.WXR.Post(post)
		if it == nil {
			t.Fatalf("the WXR has no item %d", post)
		}
		id, err := imp.IDs.Assign(PostObject(it), content.RefMedia)
		if err != nil {
			t.Fatal(err)
		}
		imp.AddRef(content.RefMedia, id, content.Ref{Type: content.MediaImage})
		ids[post] = id
	}
	return ids
}

// mediaURLsWant is the paths of the files of the attachment with the post ID post, which MediaURLs gives.
type mediaURLsWant struct {
	post  int64
	paths []string
}

func wantMediaURLs(ids map[int64]string, groups ...mediaURLsWant) []MediaURL {
	var want []MediaURL
	for _, g := range groups {
		for _, p := range g.paths {
			want = append(want, MediaURL{Path: p, Post: g.post, Media: ids[g.post]})
		}
	}
	slices.SortFunc(want, func(a, b MediaURL) int { return strings.Compare(a.Path, b.Path) })
	return want
}

func checkMediaURLs(t *testing.T, imp *Import, want []MediaURL) {
	t.Helper()
	if got := imp.MediaURLs(); !slices.Equal(got, want) {
		t.Errorf("MediaURLs = %+v\nwant %+v", got, want)
	}
}

func uploadsAttachment(id int64, link, attached, attachedURL string, meta ...wxr.Meta) wxr.Item {
	it := wxr.Item{ID: id, Type: "attachment", Status: "inherit", Link: link, AttachmentURL: attachedURL}
	if attached != "" {
		it.Meta = append(it.Meta, wxr.Meta{Key: "_wp_attached_file", Value: attached})
	}
	it.Meta = append(it.Meta, meta...)
	return it
}

func attachmentMetadata(value string) wxr.Meta {
	return wxr.Meta{Key: "_wp_attachment_metadata", Value: value}
}

func sizesMeta(files ...string) wxr.Meta {
	var pairs []string
	for i, f := range files {
		pairs = append(pairs, phpString("size"+strconv.Itoa(i)), phpArray(phpString("file"), phpString(f)))
	}
	return attachmentMetadata(phpArray(phpString("sizes"), phpArray(pairs...)))
}

func TestMediaURLs(t *testing.T) {
	const up = "https://example.com/wp-content/uploads/"
	const dir = "/wp-content/uploads/2026/10/"
	imp := New("site.xml", &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			{ID: 11, Type: "post", Status: "publish", Link: "https://example.com/2026/10/hello/"},
			uploadsAttachment(41, "https://example.com/photo/", "2026/10/photo-scaled.jpg", up+"2026/10/photo-scaled.jpg", attachmentMetadata(photoMeta)),
			uploadsAttachment(42, "https://example.com/n/", "2026/10/n.png", up+"2026/10/n.png"),
			// The previews of a PDF are in its sizes.
			uploadsAttachment(43, "https://example.com/guide/", "2026/10/guide.pdf", up+"2026/10/guide.pdf", sizesMeta("guide-pdf.jpg", "guide-pdf-212x300.jpg")),
			uploadsAttachment(44, "https://example.com/dropped/", "2026/10/dropped.png", up+"2026/10/dropped.png"),
			uploadsAttachment(45, "https://example.com/unplanned/", "2026/10/unplanned.png", up+"2026/10/unplanned.png"),
			uploadsAttachment(46, "https://example.com/same/", "2026/10/same.png", up+"2026/10/same.png"),
			uploadsAttachment(47, "https://example.com/same-2/", "2026/10/same.png", up+"2026/10/same.png", sizesMeta("same-100x100.png")),
			uploadsAttachment(48, "https://example.com/cdn/", "2026/10/cdn.png", "https://cdn.example.org/wp-content/uploads/2026/10/cdn.png"),
			uploadsAttachment(49, "https://example.com/?attachment_id=49", "2026/10/報告.pdf", up+"2026/10/%E5%A0%B1%E5%91%8A.pdf"),
			uploadsAttachment(50, "https://example.com/twice/", "2026/10/twice.png", up+"2026/10/twice.png", sizesMeta("twice-1.png", "twice-1.png", "twice.png")),
			uploadsAttachment(51, "https://example.com/no-url/", "2026/10/no-url.png", ""),
		},
	}, NewReport(Info{}))
	ids := planMedia(t, imp, 41, 42, 43, 44, 46, 47, 48, 49, 50, 51)
	// Media takes out what it does not import.
	imp.IDs.Drop(PostObject(imp.WXR.Post(44)))
	imp.DropRef(ids[44])
	delete(ids, 44)

	want := wantMediaURLs(ids,
		mediaURLsWant{41, []string{dir + "photo-scaled.jpg", dir + "photo-300x200-scaled.jpg", dir + "photo-150x150.jpg", dir + "photo.jpg"}},
		mediaURLsWant{42, []string{dir + "n.png"}},
		mediaURLsWant{43, []string{dir + "guide.pdf", dir + "guide-pdf.jpg", dir + "guide-pdf-212x300.jpg"}},
		mediaURLsWant{46, []string{dir + "same.png"}},
		mediaURLsWant{47, []string{dir + "same-100x100.png"}},
		mediaURLsWant{49, []string{dir + "報告.pdf"}},
		mediaURLsWant{50, []string{dir + "twice.png", dir + "twice-1.png"}},
	)
	checkMediaURLs(t, imp, want)
	if !slices.IsSortedFunc(want, func(a, b MediaURL) int { return strings.Compare(a.Path, b.Path) }) {
		t.Errorf("the paths are not in order: %+v", want)
	}

	// The path that two attachments have is that of the first, as the conversion of the links reads it.
	x := imp.urlIndex()
	for _, tt := range []struct {
		path string
		id   int64
	}{{dir + "same.png", 46}, {dir + "same-100x100.png", 47}} {
		if id, _, _ := x.lookup(&url.URL{Scheme: "https", Host: "example.com", Path: tt.path}); id != tt.id {
			t.Errorf("lookup(%s) = %d, want %d", tt.path, id, tt.id)
		}
	}
	// Every path is the address of its attachment in the index, and none is that of the page of an attachment.
	for _, m := range imp.MediaURLs() {
		if id, sitePath, same := x.lookup(&url.URL{Scheme: "https", Host: "example.com", Path: m.Path}); id != m.Post || !same || sitePath != m.Path {
			t.Errorf("lookup of the path %q = %d, %q, %t, want the post %d", m.Path, id, sitePath, same, m.Post)
		}
	}
	for _, it := range imp.WXR.Items {
		if p, ok := linkPath(x.home, it.Link); ok && slices.ContainsFunc(imp.MediaURLs(), func(m MediaURL) bool { return m.Path == p }) {
			t.Errorf("the path %q of the page of item %d is in MediaURLs", p, it.ID)
		}
	}
}

func TestMediaURLsNeedAMedium(t *testing.T) {
	const up = "https://example.com/wp-content/uploads/"
	imp := New("site.xml", &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			uploadsAttachment(1, "https://example.com/a/", "2026/10/a.png", up+"2026/10/a.png"),
			uploadsAttachment(2, "https://example.com/b/", "2026/10/b.png", up+"2026/10/b.png"),
			uploadsAttachment(3, "https://example.com/c/", "2026/10/c.png", up+"2026/10/c.png"),
		},
	}, NewReport(Info{}))
	if got := imp.MediaURLs(); got != nil {
		t.Errorf("MediaURLs before the plan = %+v, want none", got)
	}
	// An attachment that is imported as an entry is not a medium, and a medium with no reference, or with that of an entry,
	// is not one that was made.
	if _, err := imp.IDs.Assign(PostObject(imp.WXR.Post(3)), content.RefEntry); err != nil {
		t.Fatal(err)
	}
	idA, err := imp.IDs.Assign(PostObject(imp.WXR.Post(1)), content.RefMedia)
	if err != nil {
		t.Fatal(err)
	}
	if got := imp.MediaURLs(); got != nil {
		t.Errorf("MediaURLs of a medium with no reference = %+v, want none", got)
	}
	imp.AddRef(content.RefEntry, idA, content.Ref{Type: content.TypePage})
	if got := imp.MediaURLs(); got != nil {
		t.Errorf("MediaURLs of a medium with the reference of an entry = %+v, want none", got)
	}
	imp.AddRef(content.RefMedia, idA, content.Ref{Type: content.MediaImage})
	checkMediaURLs(t, imp, []MediaURL{{Path: "/wp-content/uploads/2026/10/a.png", Post: 1, Media: idA}})
}

func TestMediaURLsHomeWithAPath(t *testing.T) {
	imp := New("site.xml", &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com/blog/"},
		Items: []wxr.Item{
			uploadsAttachment(1, "https://example.com/blog/a/", "2026/a.png", "https://example.com/blog/wp-content/uploads/2026/a.png", sizesMeta("a-150x150.png")),
			// Not under the home.
			uploadsAttachment(2, "https://example.com/blog/b/", "2026/b.png", "https://example.com/other/wp-content/uploads/2026/b.png"),
			// More than one slash, as the WXR of a site whose uploads URL ends with a slash has.
			uploadsAttachment(3, "https://example.com/blog/c/", "2026/c.png", "https://example.com/blog/wp-content/uploads//2026//c.png"),
		},
	}, NewReport(Info{}))
	ids := planMedia(t, imp, 1, 2, 3)
	checkMediaURLs(t, imp, wantMediaURLs(ids,
		mediaURLsWant{1, []string{"/wp-content/uploads/2026/a.png", "/wp-content/uploads/2026/a-150x150.png"}},
		mediaURLsWant{3, []string{"/wp-content/uploads/2026/c.png"}},
	))
}

func TestMediaURLsFileNames(t *testing.T) {
	// A name is kept as the WXR writes it: an NFD name is not made NFC, as the file name of a medium is.
	nfd := string([]rune{0x304b, 0x3099}) // the kana ka and the combining dakuten
	nfc := string([]rune{0x304c})         // the kana ga
	const up = "https://example.com/wp-content/uploads/2026/10/"
	imp := New("site.xml", &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items: []wxr.Item{
			uploadsAttachment(1, "https://example.com/a/", "2026/10/"+nfd+".png", up+url.PathEscape(nfd)+".png"),
			uploadsAttachment(2, "https://example.com/b/", "2026/10/"+nfc+".png", up+nfc+".png"),
		},
	}, NewReport(Info{}))
	ids := planMedia(t, imp, 1, 2)
	got := imp.MediaURLs()
	if len(got) != 2 || got[0].Path == got[1].Path {
		t.Fatalf("MediaURLs = %+v, want two different paths", got)
	}
	for _, m := range got {
		name := m.Path[strings.LastIndex(m.Path, "/")+1:]
		if want := map[int64]string{1: nfd, 2: nfc}[m.Post]; name != want+".png" || m.Media != ids[m.Post] {
			t.Errorf("MediaURLs has %q for the post %d with the medium %s, want %q", name, m.Post, m.Media, want+".png")
		}
	}
}

func TestMediaURLsOfAPageThatIsTheFile(t *testing.T) {
	// A plugin that gives an attachment the address of its file as its link makes the page the file, which is still the file.
	const file = "https://example.com/wp-content/uploads/2026/10/a.png"
	imp := New("site.xml", &wxr.Export{
		Channel: wxr.Channel{Link: "https://example.com"},
		Items:   []wxr.Item{uploadsAttachment(1, file, "2026/10/a.png", file)},
	}, NewReport(Info{}))
	ids := planMedia(t, imp, 1)
	checkMediaURLs(t, imp, wantMediaURLs(ids, mediaURLsWant{1, []string{"/wp-content/uploads/2026/10/a.png"}}))
}

func TestMediaURLsWithoutAHome(t *testing.T) {
	imp := New("site.xml", &wxr.Export{Items: []wxr.Item{
		uploadsAttachment(1, "https://example.com/a/", "2026/10/a.png", "https://example.com/wp-content/uploads/2026/10/a.png"),
	}}, NewReport(Info{}))
	planMedia(t, imp, 1)
	if got := imp.MediaURLs(); got != nil {
		t.Errorf("MediaURLs of a WXR with no home page = %+v, want none", got)
	}
}

func TestMediaURLsAfterMedia(t *testing.T) {
	imp := newSampleMediaImport(t, true)
	docs := runMedia(t, imp, MediaOptions{Files: filesDir(t), Uploads: uploads(mediaSampleCopy()), Workers: 2})
	ids := make(map[int64]string)
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if _, id, ok := imp.IDs.Lookup(PostObject(it)); ok {
			ids[it.ID] = id
		}
	}
	if len(docs) != 9 || len(ids) != 9 {
		t.Fatalf("Media gives %d documents and the table has %d media, want 9", len(docs), len(ids))
	}
	// 209, 210 and 211 are not imported, and the pages of the attachments are not files.
	const dir = "/wp-content/uploads/2026/10/"
	checkMediaURLs(t, imp, wantMediaURLs(ids,
		mediaURLsWant{201, []string{dir + "office-scaled.jpg", dir + "office-150x150.jpg", dir + "office-300x200.jpg", dir + "office.jpg"}},
		mediaURLsWant{202, []string{dir + "store-rotated.jpg", dir + "store.jpg"}},
		mediaURLsWant{203, []string{dir + "logo.png", dir + "logo-150x150.png"}},
		mediaURLsWant{204, []string{dir + "anim.gif"}},
		mediaURLsWant{205, []string{dir + "guide.pdf"}},
		mediaURLsWant{206, []string{dir + "logo-copy.png"}},
		mediaURLsWant{207, []string{dir + "team-scaled-e1760000000123.jpg", dir + "team.jpg"}},
		mediaURLsWant{208, []string{dir + "beach-scaled.jpg", dir + "beach.heic"}},
		mediaURLsWant{212, []string{dir + "写真.png"}},
	))
}
