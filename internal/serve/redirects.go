package serve

import (
	"cmp"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// redirectIndex finds the redirect that applies to a request (6.6.6), among redirects whose froms are in the canonical
// form. It has them by the paths of their froms without a trailing "/" (trimSlash), as Redirect.Matches compares paths.
type redirectIndex map[string]*redirectBucket

// redirectBucket is the redirects whose froms have the same path: those without a query, and the others by their first
// query parameter, which a request must have for them to match.
type redirectBucket struct {
	plain   []*serverRedirect
	byParam map[string][]*serverRedirect
}

func newRedirectIndex(rs []serverRedirect) redirectIndex {
	x := make(redirectIndex)
	for i := range rs {
		r := &rs[i]
		path, query, _ := strings.Cut(r.From, "?")
		path = trimSlash(path)
		b := x[path]
		if b == nil {
			b = &redirectBucket{byParam: make(map[string][]*serverRedirect)}
			x[path] = b
		}
		if query == "" {
			b.plain = append(b.plain, r)
		} else {
			first, _, _ := strings.Cut(query, "&")
			b.byParam[first] = append(b.byParam[first], r)
		}
	}
	return x
}

// match returns the redirect that applies to target, the path of a request from the site URL with its query, if any;
// nil if none does. Of the redirects that match target, the one with the most query parameters applies, and of as many,
// the one with the smallest from.
func (x redirectIndex) match(target string) *serverRedirect {
	t, _, _ := strings.Cut(target, "#")
	path, query, _ := strings.Cut(content.CanonicalFrom(t), "?")
	b := x[trimSlash(path)]
	if b == nil {
		return nil
	}
	var best *serverRedirect
	consider := func(rs []*serverRedirect) {
		for _, r := range rs {
			if (content.Redirect{From: r.From}).Matches(target) && (best == nil || comparePrecedence(r.From, best.From) < 0) {
				best = r
			}
		}
	}
	consider(b.plain)
	if query != "" {
		for p := range strings.SplitSeq(query, "&") {
			consider(b.byParam[p])
		}
	}
	return best
}

// trimSlash removes a trailing "/" from path, unless path is "/", as Redirect.Matches compares paths.
func trimSlash(path string) string {
	if path == "/" {
		return path
	}
	return strings.TrimSuffix(path, "/")
}

// comparePrecedence orders a and b, froms in the canonical form, by their precedence when both match a request
// (6.6.6): the one with more query parameters first, and then the smaller one.
func comparePrecedence(a, b string) int {
	return cmp.Or(cmp.Compare(paramCount(b), paramCount(a)), strings.Compare(a, b))
}

// paramCount returns the number of the query parameters of from, a from in the canonical form.
func paramCount(from string) int {
	if _, query, _ := strings.Cut(from, "?"); query != "" {
		return strings.Count(query, "&") + 1
	}
	return 0
}
