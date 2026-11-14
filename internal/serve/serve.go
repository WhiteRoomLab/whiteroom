// Package serve is the built-in public server (9.5), which serves the releases that the build publishes.
package serve

import (
	"cmp"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
)

// contentSecurityPolicy is sent with every response. Scripts and styles must be files of the site, so inline ones are
// blocked; images and frames are not limited.
const contentSecurityPolicy = "script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'self'"

// Handler serves the public directory of the release that out/current points to, with the redirects, the 404 pages
// and the directory index of its server.json. It reads out/current for every request, so that a new build is served
// from the next request, server.json included.
type Handler struct {
	out *os.Root
	log *log.Logger

	mu  sync.Mutex
	rel *release // the last one loaded
}

// New returns a Handler for out, the output directory of builds. It fails if out has no published release. errorLog
// gets the errors that requests run into, such as an invalid server.json; nil discards them.
func New(out string, errorLog *log.Logger) (*Handler, error) {
	abs, err := fspath.Abs(out)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	if errorLog == nil {
		errorLog = log.New(io.Discard, "", 0)
	}
	h := &Handler{out: root, log: errorLog}
	if _, err := h.current(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return h, nil
}

// Base returns the path of the site URL of the published release in the canonical form of froms, such as "/blog"; ""
// at the root of its host.
func (h *Handler) Base() (string, error) {
	rel, err := h.current()
	if err != nil {
		return "", err
	}
	return rel.base, nil
}

// Close closes out.
func (h *Handler) Close() error {
	return h.out.Close()
}

// ServeHTTP answers GET and HEAD requests for the paths in the site URL of the release, in this order: with the
// redirect of server.json that applies (6.6.6), with the file of the path, or with the 404 page. Paths outside the
// site URL get a plain 404, and the errors of the release 500 (503 without a release), which errorLog gets.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel, err := h.current()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errNoRelease) {
			status = http.StatusServiceUnavailable
		}
		h.fail(w, r, status, err)
		return
	}
	// As sent: EscapedPath encodes Path again, without "%2F" or "%24", if RawPath has bytes such as "|" or UTF-8.
	p := cmp.Or(r.URL.RawPath, r.URL.EscapedPath(), "/")
	query := ""
	if r.URL.RawQuery != "" {
		query = "?" + r.URL.RawQuery
	}
	rest, atBase, ok := cutBase(rel.baseSegs, p)
	if !ok {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	if atBase {
		redirect(w, p+"/"+query, http.StatusMovedPermanently)
		return
	}
	if x := rel.redirects.match(rest + query); x != nil {
		redirect(w, x.To, x.Status)
		return
	}
	h.serveFile(w, r, rel, rest, p+"/"+query)
}

// cutBase returns the rest of p, the path of a request as it was sent, after the path of the site URL, whose segments
// are base (decoded; none at the root of its host), in the canonical form of froms; and atBase if p is the path of the
// site URL without the trailing "/". ok is false if p is outside the site URL. The segments are compared decoded, as
// directories: the canonical form keeps "%28" and "(" apart, but links have "/a%28b%29/" for the site URL "/a(b)".
func cutBase(base []string, p string) (rest string, atBase, ok bool) {
	if len(base) == 0 {
		return content.CanonicalFrom(p), false, true
	}
	s, found := strings.CutPrefix(p, "/")
	if !found {
		return "", false, false
	}
	parts := strings.SplitN(s, "/", len(base)+1)
	if len(parts) < len(base) {
		return "", false, false
	}
	for i, seg := range base {
		if d, err := url.PathUnescape(parts[i]); err != nil || d != seg {
			return "", false, false
		}
	}
	if len(parts) == len(base) {
		return "", true, true
	}
	return content.CanonicalFrom("/" + parts[len(base)]), false, true
}

// redirect answers with status and the Location loc as it is, without a body (http.Redirect writes one, and cleans
// paths).
func redirect(w http.ResponseWriter, loc string, status int) {
	w.Header().Set("Location", loc)
	w.WriteHeader(status)
}

// fail logs err, which r has run into, and answers r with status and its text.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, status int, err error) {
	h.logError(r, err)
	http.Error(w, strconv.Itoa(status)+" "+strings.ToLower(http.StatusText(status)), status)
}

// logError logs err, which r has run into.
func (h *Handler) logError(r *http.Request, err error) {
	// Quoted: the request-target may have bytes such as those of C1 controls or of bidirectional overrides.
	h.log.Printf("%s %q: %v", r.Method, r.RequestURI, err)
}
