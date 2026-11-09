package wpimport

import (
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// urlIndex finds the post of WordPress that a URL of the old site is the address of: a page, an entry, an attachment
// page, or an attachment file.
type urlIndex struct {
	home  *url.URL
	base  string           // the path of the home, in the form of content.CanonicalFrom, without a slash at its end
	paths map[string]int64 // the path in the site, in the form of content.CanonicalFrom and with no run of slashes -> the ID of the post
}

// urlIndex returns the index of the URLs of the WXR, which it makes when it is first needed.
func (imp *Import) urlIndex() *urlIndex {
	if imp.urls == nil {
		imp.urls = newURLIndex(imp.WXR)
	}
	return imp.urls
}

func newURLIndex(e *wxr.Export) *urlIndex {
	x := &urlIndex{paths: make(map[string]int64)}
	if e == nil {
		return x
	}
	x.home = parseHome(e.Channel.Link)
	if x.home == nil {
		x.home = parseHome(e.Channel.BaseBlogURL)
	}
	if x.home == nil {
		return x
	}
	x.base = homeBase(x.home)
	for i := range e.Items {
		it := &e.Items[i]
		if p, ok := linkPath(x.home, it.Link); ok {
			x.add(p, it.ID)
		}
		if it.Type == "attachment" {
			x.addFiles(it)
		}
	}
	return x
}

// add records that the path is the address of the post id, unless an earlier post has it. The path is kept as lookup reads an
// address, with each run of slashes made one.
func (x *urlIndex) add(p string, id int64) {
	p = collapseSlashes(p)
	if _, ok := x.paths[p]; !ok {
		x.paths[p] = id
	}
}

// rawPath writes the path p, which is not escaped, in the form of the paths of the index.
func rawPath(p string) string {
	return content.CanonicalFrom((&url.URL{Path: p}).EscapedPath())
}

// addFiles records the files of the attachment it: the one that wp:attachment_url has, and the others that are next to
// it, by the names that the metadata gives: the smaller sizes, and the original image of a scaled one.
func (x *urlIndex) addFiles(it *wxr.Item) {
	main, ok := linkPath(x.home, it.AttachmentURL)
	if !ok {
		return
	}
	x.add(main, it.ID)
	attached, _ := it.Meta.Get("_wp_attached_file")
	attached = strings.TrimLeft(strings.TrimSpace(attached), "/")
	if attached == "" || strings.ContainsAny(attached, "?#") {
		return
	}
	// The files are in the directory of the attached file, in the place of the uploads that the address tells.
	uploads, ok := strings.CutSuffix(main, rawPath("/"+attached))
	if !ok {
		return
	}
	dir := path.Dir("/" + attached)
	for _, name := range x.fileNames(it) {
		if name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/?#") {
			x.add(uploads+rawPath(path.Join(dir, name)), it.ID)
		}
	}
}

// fileNames returns the names of the files that the metadata of the attachment gives, or none if it cannot be read.
func (x *urlIndex) fileNames(it *wxr.Item) []string {
	value, ok := it.Meta.Get("_wp_attachment_metadata")
	if !ok {
		return nil
	}
	v, err := Unserialize(nil, Source{}, wxr.Meta{Key: "_wp_attachment_metadata", Value: value})
	if err != nil {
		return nil
	}
	var names []string
	if sizes, ok := v.Lookup("sizes"); ok && sizes.Kind == phpserialize.Array {
		for _, e := range sizes.Array {
			if f, ok := e.Value.Lookup("file"); ok && f.Kind == phpserialize.String {
				names = append(names, f.Str)
			}
		}
	}
	if o, ok := v.Lookup("original_image"); ok && o.Kind == phpserialize.String {
		names = append(names, o.Str)
	}
	return names
}

// lookup tells whether u, an absolute URL, is an address in the site of the WXR: its scheme is http or https, and its
// host and port are those of the home, and its path is under the home's. sitePath is then the path after that of the
// home, in the form of content.CanonicalFrom, with each run of slashes made one, such as "/about/", and id is the post
// that it is the address of in WordPress, or 0 if it is not that of one. The home with a query is the address of a post
// only if the query names it (p, page_id, attachment_id); the other queries, such as ?cat=3 and ?s=tea, are lists that
// WordPress makes, not the front page.
func (x *urlIndex) lookup(u *url.URL) (id int64, sitePath string, same bool) {
	if x.home == nil {
		return 0, "", false
	}
	sitePath, ok := sitePathOf(x.home, x.base, u)
	if !ok {
		return 0, "", false
	}
	sitePath = collapseSlashes(sitePath)
	if (sitePath == "/" || sitePath == "/index.php") && u.RawQuery != "" {
		return plainID(u.RawQuery), sitePath, true
	}
	id = x.paths[sitePath]
	if id == 0 && sitePath != "/" {
		if alt, ok := strings.CutSuffix(sitePath, "/"); ok {
			id = x.paths[alt]
		} else {
			id = x.paths[sitePath+"/"]
		}
	}
	return id, sitePath, true
}

// collapseSlashes makes each run of slashes in p one, as the servers read a path.
func collapseSlashes(p string) string {
	if !strings.Contains(p, "//") {
		return p
	}
	var b strings.Builder
	for i := range len(p) {
		if p[i] != '/' || i == 0 || p[i-1] != '/' {
			b.WriteByte(p[i])
		}
	}
	return b.String()
}

// plainID returns the number in the first of p, page_id and attachment_id in the query that is a number of 1 or more
// and the first of them that is written, or 0.
func plainID(query string) int64 {
	for pair := range strings.SplitSeq(query, "&") {
		key, value, _ := strings.Cut(pair, "=")
		if k, err := url.QueryUnescape(key); err != nil || (k != "p" && k != "page_id" && k != "attachment_id") {
			continue
		}
		v, err := url.QueryUnescape(value)
		if err != nil || !isDigits(v) {
			return 0
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}
		return id
	}
	return 0
}
