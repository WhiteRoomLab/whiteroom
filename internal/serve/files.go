package serve

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// contentTypes are the Content-Types of the files by their extensions in lower case, in a table of their own:
// mime.TypeByExtension reads the tables of the system. Other files are application/octet-stream.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".xml":         "application/xml",
	".txt":         "text/plain; charset=utf-8",
	".csv":         "text/csv; charset=utf-8",
	".pdf":         "application/pdf",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".png":         "image/png",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".svg":         "image/svg+xml",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".mp4":         "video/mp4",
	".webm":        "video/webm",
	".mp3":         "audio/mpeg",
	".wasm":        "application/wasm",
}

func contentType(name string) string {
	if t, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// serveFile answers r with the file of rest, a path from the site URL in the canonical form, in the public directory
// of rel (find); a directory gets a redirect to slash, the path of r with "/", and a path without a file the 404 page.
func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, rel *release, rest, slash string) {
	public := "releases/" + rel.name + "/public"
	pub, err := h.out.OpenRoot(public)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, fileError(h.out, public, err))
		return
	}
	defer func() { _ = pub.Close() }()
	name, fi, err := find(pub, rest, rel.index)
	switch {
	case err != nil:
		h.fail(w, r, http.StatusInternalServerError, fileError(pub, name, err))
		return
	case fi == nil:
		h.notFound(w, r, pub, rel.notFound, rest)
		return
	case fi.IsDir():
		redirect(w, slash, http.StatusMovedPermanently)
		return
	}
	f, err := pub.Open(name)
	if err != nil {
		h.fail(w, r, http.StatusInternalServerError, fileError(pub, name, err))
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentType(name))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// find returns the name in pub of the file of rest, a path from the site URL in the canonical form, with its FileInfo:
// a regular file; for a path that ends with "/", the first name of index in that directory that is a regular file; or
// a directory, for a path without the trailing "/". It returns no FileInfo if there is none or if rest is not the path
// of a file to serve (fileName), and the name with the error of another problem than a missing file.
func find(pub *os.Root, rest string, index []string) (string, fs.FileInfo, error) {
	name, dir, ok := fileName(rest)
	if !ok {
		return "", nil, nil
	}
	names := []string{name}
	if dir {
		names = make([]string, len(index))
		for i, x := range index {
			// Not name+"/"+x: name is "" for the directory itself.
			names[i] = path.Join(name, x)
		}
	}
	for _, n := range names {
		fi, err := stat(pub, n)
		if err != nil || fi != nil && (fi.Mode().IsRegular() || fi.IsDir() && !dir) {
			return n, fi, err
		}
	}
	return "", nil, nil
}

// stat returns the FileInfo of name in pub, following the symbolic links that stay in pub; nil if name does not exist,
// also because a file in its path is not a directory.
func stat(pub *os.Root, name string) (fs.FileInfo, error) {
	fi, err := pub.Stat(name)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	return fi, err
}

// notFound answers r with the 404 page of rest, a path from the site URL in the canonical form: that of the first of
// pages whose prefix starts rest. Without one, or if its file is missing or not a regular file, the response is a
// plain 404.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request, pub *os.Root, pages []notFoundPage, rest string) {
	i := slices.IndexFunc(pages, func(p notFoundPage) bool { return strings.HasPrefix(rest, p.Prefix) })
	if i < 0 || !h.servePage(w, r, pub, pages[i].Page) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	}
}

// servePage answers r with the 404 page at page, a path from the site URL, and reports whether it has: not if its file
// in pub is missing or not a regular file. Other problems are logged.
func (h *Handler) servePage(w http.ResponseWriter, r *http.Request, pub *os.Root, page string) bool {
	name, _, _ := fileName(page)
	f, fi, err := openRegular(pub, name)
	if err != nil {
		h.logError(r, fileError(pub, name, err))
	}
	if f == nil {
		return false
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, f)
	}
	return true
}

// openRegular opens name in pub, and returns it with its FileInfo, if it is a regular file; nil if it does not exist or
// is another kind of file, such as a named pipe, which opening would wait on.
func openRegular(pub *os.Root, name string) (*os.File, fs.FileInfo, error) {
	fi, err := stat(pub, name)
	if err != nil || fi == nil || !fi.Mode().IsRegular() {
		return nil, nil, err
	}
	f, err := pub.Open(name)
	if err != nil {
		return nil, nil, err
	}
	return f, fi, nil
}

// fileError returns err, from name in root, after the path of name instead of the operation and the relative path of
// an *fs.PathError.
func fileError(root *os.Root, name string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%s: %w", filepath.Join(root.Name(), filepath.FromSlash(name)), err)
}

// fileName returns the name in the public directory of rest, the path of a request from the site URL: its segments,
// decoded, joined by "/" ("" for "/"); and whether rest ends with "/", for a directory. It is not ok if rest does not
// start with "/", or if a segment does not decode or is not a plainName once decoded, such as an empty segment ("//x"),
// a dot segment, a hidden file (".htaccess") or an encoded "/".
func fileName(rest string) (name string, dir, ok bool) {
	if !strings.HasPrefix(rest, "/") {
		return "", false, false
	}
	dir = strings.HasSuffix(rest, "/")
	segs := strings.Split(rest[1:], "/")
	if dir {
		segs = segs[:len(segs)-1]
	}
	for i, s := range segs {
		d, err := url.PathUnescape(s)
		if err != nil || !plainName(d) {
			return "", false, false
		}
		segs[i] = d
	}
	return strings.Join(segs, "/"), dir, true
}

// plainName reports whether s can be the name of a file or a directory that the server serves: not empty, not hidden
// (which "." and ".." also are), in UTF-8 without "/" or control characters, and at most content.MaxFileNameBytes
// long, as the build makes names (systems may reject other names with another error than fs.ErrNotExist).
func plainName(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.Contains(s, "/") && utf8.ValidString(s) &&
		!strings.ContainsFunc(s, unicode.IsControl) && len(s) <= content.MaxFileNameBytes
}
