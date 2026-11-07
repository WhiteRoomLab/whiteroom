package siteplan

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Redirect is a redirect that the servers serve: one of the site with its target resolved, or one that is generated.
type Redirect struct {
	From      string // in the canonical form of froms, from the site URL, such as "/?p=123"
	To        string // a path from the site URL, such as "/news/" (as written if To is a path), or an absolute URL
	Abs       bool   // To is an absolute URL
	Status    int
	Src       Source // the from of the redirect, or the item of the settings that generates it
	Generated bool
}

// ResolveRedirects returns the redirects of the site with their targets resolved (6.6.6), and those that are generated
// for the front pages (frontRedirects) whose froms neither those redirects nor the outputs of p have (as FromKey
// compares them), sorted by from. The ID of an entity becomes the path of its page in p: that of an entry (the front
// page at the path of its language, the posts page at its list), the list of a term, or the author page in the default
// language; or the path of the published file of a medium, which mediaPath returns. A redirect to an entry that is not
// published is left out, with a warning to warn. The errors, such as those of mediaPath, go to report. It needs s.Site.
func (s *Site) ResolveRedirects(p *Plan, mediaPath func(id string) (string, error), warn, report Report) []Redirect {
	var rs []Redirect
	for _, id := range slices.Sorted(maps.Keys(s.Redirects)) {
		d := s.Redirects[id]
		src := Source{File: d.File, Pointer: "/from", What: d.File.Name}
		r := Redirect{From: d.V.From, To: d.V.To, Status: d.V.Status, Src: src}
		switch to := d.V.To; {
		case strings.HasPrefix(to, "/"):
		case strings.Contains(to, ":"):
			r.Abs = true
		case s.Entries[to] != nil:
			path, ok := p.EntryPaths[to]
			if !ok {
				warn.addf(d.File, "/to", "entry %q is not published; the redirect is left out", to)
				continue
			}
			r.To = path
		case s.Terms[to] != nil:
			r.To = p.TermPaths[to]
		case s.Authors[to] != nil:
			r.To = p.AuthorPaths[to][s.Site.DefaultLanguage]
		default: // a medium, the only other target that validation accepts
			path, err := mediaPath(to)
			if err != nil {
				report.addf(d.File, "/to", "cannot make the path: %v", err)
				continue
			}
			r.To = path
		}
		rs = append(rs, r)
	}
	taken := make(map[string]bool)
	for _, r := range rs {
		taken[FromKey(r.From)] = true
	}
	for _, o := range p.Outputs {
		taken[FromKey(o.Path)] = true
	}
	for _, r := range s.frontRedirects(report) {
		if k := FromKey(r.From); !taken[k] {
			taken[k] = true
			rs = append(rs, r)
		}
	}
	slices.SortStableFunc(rs, func(x, y Redirect) int { return strings.Compare(x.From, y.From) })
	return rs
}

// frontRedirects returns the redirects that are generated to the front pages: with prefixDefaultLanguage, from "/" to
// the path of the default language with 302, so that how a language is chosen there can change; and from the path of
// each frontPage by its URL rule, such as "/home/", to the path of its language with 301. The errors of the paths go to
// report.
func (s *Site) frontRedirects(report Report) []Redirect {
	var rs []Redirect
	add := func(from, lang, pointer string, status int) {
		src := Source{File: s.Settings, Pointer: pointer, What: "the redirect to the front page in " + lang}
		rs = append(rs, Redirect{From: from, To: s.Site.LanguagePath(lang), Status: status, Src: src, Generated: true})
	}
	if s.Site.PrefixDefaultLanguage {
		add("/", s.Site.DefaultLanguage, "/prefixDefaultLanguage", 302)
	}
	for _, lang := range s.Site.Languages {
		d := s.Published(s.Site.FrontPage[lang])
		if d == nil {
			continue
		}
		path, err := s.Site.EntryRulePath(&d.E, d.Rev, s.PathRef)
		if err != nil {
			report.addf(d.RevFile, "/slug", "cannot make the path: %v", err)
			continue
		}
		add(content.CanonicalFrom(path), lang, "/frontPage/"+lang, 301)
	}
	return rs
}

// TrimSlash removes a trailing "/" from path, unless path is "/", as Redirect.Matches compares paths.
func TrimSlash(path string) string {
	if path == "/" {
		return path
	}
	return strings.TrimSuffix(path, "/")
}

// splitFrom returns s, a from or another path in the site with an optional query and no fragment, such as the path of
// a page or the target of a redirect, as Redirect.Matches compares it: the path in the canonical form of froms, without
// a trailing "/" unless it is "/", and the query parameters in the canonical form and order.
func splitFrom(s string) (path string, params []string) {
	path, query, _ := strings.Cut(content.CanonicalFrom(s), "?")
	if query != "" {
		params = strings.Split(query, "&")
	}
	return TrimSlash(path), params
}

// FromKey returns s as splitFrom does, in one string. Two froms have the same key if and only if they match the same
// requests; a from with a query never has the key of one without.
func FromKey(s string) string {
	path, params := splitFrom(s)
	if len(params) == 0 {
		return path
	}
	return path + "?" + strings.Join(params, "&")
}

// ComparePrecedence orders a and b, froms in the canonical form, by their precedence when both match a request
// (6.6.6): the one with more query parameters first, and then the smaller one.
func ComparePrecedence(a, b string) int {
	return cmp.Or(cmp.Compare(paramCount(b), paramCount(a)), strings.Compare(a, b))
}

// paramCount returns the number of the query parameters of from, a from in the canonical form.
func paramCount(from string) int {
	if _, query, _ := strings.Cut(from, "?"); query != "" {
		return strings.Count(query, "&") + 1
	}
	return 0
}

// redirectIndex finds the redirect that applies to a request.
type redirectIndex struct {
	redirects map[string]*Doc[content.Redirect]
	ids       map[string][]string // in ID order, by the path of splitFrom and the first query parameter: "/a?" or "/a?x=1"
}

func newRedirectIndex(redirects map[string]*Doc[content.Redirect]) redirectIndex {
	x := redirectIndex{redirects: redirects, ids: make(map[string][]string)}
	for _, id := range slices.Sorted(maps.Keys(redirects)) {
		path, params := splitFrom(redirects[id].V.From)
		k := path + "?"
		if len(params) > 0 {
			k += params[0]
		}
		x.ids[k] = append(x.ids[k], id)
	}
	return x
}

// match returns the ID of the redirect that applies to target, a path in the site with an optional query and no
// fragment, or "" if none does: of the redirects that match target, the first by ComparePrecedence, and then by ID.
// Only the redirects whose froms have no query or a first parameter that target has can match it.
func (x redirectIndex) match(target string) string {
	path, params := splitFrom(target)
	best := ""
	for _, param := range slices.Concat([]string{""}, params) {
		for _, id := range x.ids[path+"?"+param] {
			r := x.redirects[id].V
			if r.Matches(target) && (best == "" || ComparePrecedence(r.From, x.redirects[best].V.From) < 0) {
				best = id
			}
		}
	}
	return best
}
