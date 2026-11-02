package serve

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// serverList is server.json, which the build writes into each release (6.6.6).
type serverList struct {
	Base      string           `json:"base"` // the path of the site URL in the canonical form of froms; "" at the root
	Redirects []serverRedirect `json:"redirects"`
	NotFound  []notFoundPage   `json:"notFound"`
	Index     []string         `json:"index"`
}

// serverRedirect is a redirect in server.json.
type serverRedirect struct {
	From   string `json:"from"`
	To     string `json:"to"` // the Location of the responses
	Status int    `json:"status"`
}

// notFoundPage is the 404 page of the paths that start with Prefix, a path from the site URL that ends with "/".
type notFoundPage struct {
	Prefix string `json:"prefix"`
	Page   string `json:"page"` // its path from the site URL
}

// release is a release of the output directory with its server.json. It does not change once parseRelease has made
// it, so that requests can share it.
type release struct {
	name      string   // 16 hex digits
	base      string   // as in server.json
	baseSegs  []string // the segments of base, decoded; none at the root
	redirects redirectIndex
	notFound  []notFoundPage // the first one whose prefix starts a path applies to it
	index     []string
}

var errNoRelease = errors.New("no release is published")

// current returns the release that out/current points to, which must be a link to releases/<16 hex digits>. It reads
// the server.json of the release unless it has loaded that release last, and keeps the release only if it is valid.
func (h *Handler) current() (*release, error) {
	target, err := h.out.Readlink("current")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w in %s", errNoRelease, h.out.Name())
	case errors.Is(err, syscall.EINVAL):
		return nil, fmt.Errorf("%s: current is not a symbolic link", h.out.Name())
	case err != nil:
		return nil, fmt.Errorf("%s: %w", h.out.Name(), err)
	}
	name, ok := strings.CutPrefix(target, "releases/")
	if !ok || !isHex16(name) {
		return nil, fmt.Errorf("%s: current points to %q, not to a release", h.out.Name(), target)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rel != nil && h.rel.name == name {
		return h.rel, nil
	}
	file := "releases/" + name + "/server.json"
	data, err := h.out.ReadFile(file)
	// The path of the file comes before the error, so the operation and the relative path are left out.
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	var rel *release
	if err == nil {
		rel, err = parseRelease(name, data)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(h.out.Name(), filepath.FromSlash(file)), err)
	}
	h.rel = rel
	return rel, nil
}

// isHex16 reports whether s is 16 lower case hex digits, as the name of a release.
func isHex16(s string) bool {
	return len(s) == 16 && strings.Trim(s, "0123456789abcdef") == ""
}

// parseRelease returns the release name with data, its server.json, after checking that it is as the build writes it.
func parseRelease(name string, data []byte) (*release, error) {
	var list serverList
	if err := json.Unmarshal(data, &list, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	segs, err := baseSegments(list.Base)
	if err != nil {
		return nil, err
	}
	for i, r := range list.Redirects {
		switch {
		case !strings.HasPrefix(r.From, "/"):
			return nil, fmt.Errorf(`redirects[%d]: from %q does not start with "/"`, i, r.From)
		case content.CanonicalFrom(r.From) != r.From:
			return nil, fmt.Errorf("redirects[%d]: from %q is not in the canonical form", i, r.From)
		case r.To == "":
			return nil, fmt.Errorf("redirects[%d]: to is empty", i)
		case r.Status != 301 && r.Status != 302:
			return nil, fmt.Errorf("redirects[%d]: status %d is not 301 or 302", i, r.Status)
		}
	}
	for i, p := range list.NotFound {
		if !strings.HasPrefix(p.Prefix, "/") || !strings.HasSuffix(p.Prefix, "/") {
			return nil, fmt.Errorf(`notFound[%d]: prefix %q is not a path that ends with "/"`, i, p.Prefix)
		}
		if _, dir, ok := fileName(p.Page); !ok || dir {
			return nil, fmt.Errorf("notFound[%d]: page %q is not the path of a file to serve", i, p.Page)
		}
	}
	if len(list.Index) == 0 {
		return nil, errors.New("index is empty")
	}
	for i, f := range list.Index {
		if !plainName(f) {
			return nil, fmt.Errorf("index[%d]: %q is not the name of a file to serve", i, f)
		}
	}
	return &release{
		name:      name,
		base:      list.Base,
		baseSegs:  segs,
		redirects: newRedirectIndex(list.Redirects),
		notFound:  list.NotFound,
		index:     list.Index,
	}, nil
}

// baseSegments checks base, the path of the site URL in server.json, and returns its segments, decoded, which the
// paths of the requests in the site start with, encoded or not.
func baseSegments(base string) ([]string, error) {
	switch {
	case base == "":
		return nil, nil
	case !strings.HasPrefix(base, "/"):
		return nil, fmt.Errorf(`base %q does not start with "/"`, base)
	case strings.HasSuffix(base, "/"):
		return nil, fmt.Errorf(`base %q ends with "/"`, base)
	case strings.Contains(base, "?"):
		return nil, fmt.Errorf("base %q has a query", base)
	case content.CanonicalFrom(base) != base:
		return nil, fmt.Errorf("base %q is not in the canonical form", base)
	}
	segs := strings.Split(base[1:], "/")
	for i, s := range segs {
		d, err := url.PathUnescape(s)
		if err != nil {
			return nil, fmt.Errorf("base %q has an invalid percent-encoding", base)
		}
		segs[i] = d
	}
	return segs, nil
}
