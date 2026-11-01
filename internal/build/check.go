package build

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// checkAcross checks what the documents must agree on, the checks across entities of 6.7, adding the errors to p.
// Where two documents conflict, the later one in ID order gets the error, which names the file of the first one.
func (b *builder) checkAcross(p *problems) {
	b.checkFrontPages(p)
	b.checkSlugs(p)
	b.checkTranslationGroups(p)
	b.checkMenuLocations(p)
	b.checkParents(p)
	b.checkRedirects(p)
}

// checkFrontPages checks that the front pages and the posts pages of the settings are published.
func (b *builder) checkFrontPages(p *problems) {
	for _, pages := range []struct {
		pointer string
		ids     content.Localized
	}{{"/frontPage", b.site.FrontPage}, {"/postsPage", b.site.PostsPage}} {
		for _, l := range b.site.Languages {
			if id := pages.ids[l]; id != "" && b.published(id) == nil {
				p.add(b.settings, pages.pointer+"/"+l, "entry %q is not published", id)
			}
		}
	}
}

// checkSlugs checks that the slugs of the published entries are unique in their languages and types, and those of
// pages under their parents (6.4.2); those of the terms in their taxonomies and languages; and those of the authors.
func (b *builder) checkSlugs(p *problems) {
	type entryKey struct{ language, typ, parent, slug string }
	entries := make(map[entryKey]string)
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		d := b.published(id)
		if d == nil {
			continue
		}
		k := entryKey{language: d.e.Language, typ: d.e.Type, slug: d.rev.Slug}
		if d.e.Type == content.TypePage {
			k.parent = d.rev.Parent
		}
		if first, taken := claim(entries, k, d.revFile.Name); taken {
			p.add(d.revFile, "/slug", "slug %q is also used by %s", d.rev.Slug, first)
		}
	}
	type termKey struct{ taxonomy, language, slug string }
	terms := make(map[termKey]string)
	for _, id := range slices.Sorted(maps.Keys(b.terms)) {
		d := b.terms[id]
		if first, taken := claim(terms, termKey{d.v.Taxonomy, d.v.Language, d.v.Slug}, d.file.Name); taken {
			p.add(d.file, "/slug", "slug %q is also used by %s", d.v.Slug, first)
		}
	}
	authors := make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(b.authors)) {
		d := b.authors[id]
		if first, taken := claim(authors, d.v.Slug, d.file.Name); taken {
			p.add(d.file, "/slug", "slug %q is also used by %s", d.v.Slug, first)
		}
	}
}

// checkTranslationGroups checks that a translation group has at most one entry, of any status, and one term in each
// language. Entries without a group or a language, which drafts may be, are left out.
func (b *builder) checkTranslationGroups(p *problems) {
	type key struct{ group, language string }
	entries := make(map[key]string)
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		d := b.entries[id]
		k := key{d.e.TranslationGroup, d.e.Language}
		if k.group == "" || k.language == "" {
			continue
		}
		if first, taken := claim(entries, k, d.file.Name); taken {
			p.add(d.file, "/translationGroup", "translation group %q already has an entry in the language %q: %s",
				k.group, k.language, first)
		}
	}
	terms := make(map[key]string)
	for _, id := range slices.Sorted(maps.Keys(b.terms)) {
		d := b.terms[id]
		k := key{d.v.TranslationGroup, d.v.Language}
		if first, taken := claim(terms, k, d.file.Name); taken {
			p.add(d.file, "/translationGroup", "translation group %q already has a term in the language %q: %s",
				k.group, k.language, first)
		}
	}
}

// checkMenuLocations checks that each language has at most one menu at each location.
func (b *builder) checkMenuLocations(p *problems) {
	type key struct{ language, location string }
	menus := make(map[key]string)
	for _, id := range slices.Sorted(maps.Keys(b.menus)) {
		d := b.menus[id]
		if d.v.Location == "" {
			continue
		}
		if first, taken := claim(menus, key{d.v.Language, d.v.Location}, d.file.Name); taken {
			p.add(d.file, "/location", "location %q already has a menu in the language %q: %s",
				d.v.Location, d.v.Language, first)
		}
	}
}

// checkParents checks that the parents of the published pages are published, and that the parents of neither the
// pages nor the terms make cycles. A page or a term that leads to a cycle without being in it gets no error.
func (b *builder) checkParents(p *problems) {
	pages := make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		d := b.published(id)
		if d == nil || d.e.Type != content.TypePage || d.rev.Parent == "" {
			continue
		}
		if b.published(d.rev.Parent) == nil {
			p.add(d.revFile, "/parent", "entry %q is not published", d.rev.Parent)
		} else {
			pages[id] = d.rev.Parent
		}
	}
	for _, c := range cycles(pages) {
		for _, id := range c {
			p.add(b.entries[id].revFile, "/parent", "the parents make a cycle: %s", cycleText(c))
		}
	}
	terms := make(map[string]string)
	for id, d := range b.terms {
		if d.v.Parent != "" {
			terms[id] = d.v.Parent
		}
	}
	for _, c := range cycles(terms) {
		for _, id := range c {
			p.add(b.terms[id].file, "/parent", "the parents make a cycle: %s", cycleText(c))
		}
	}
}

// checkRedirects checks that no two redirects match the same requests, and that following redirects from one never
// leads back to it (6.6.6). A chain of redirects that ends is allowed.
func (b *builder) checkRedirects(p *problems) {
	keys := make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(b.redirects)) {
		d := b.redirects[id]
		if first, taken := claim(keys, fromKey(d.v.From), d.file.Name); taken {
			p.add(d.file, "/from", "from %q matches the same requests as that of %s", d.v.From, first)
		}
	}
	index := newRedirectIndex(b.redirects)
	next := make(map[string]string)
	for id, d := range b.redirects {
		if target, ok := b.inSitePath(d.v.To); ok {
			if n := index.match(target); n != "" {
				next[id] = n
			}
		}
	}
	for _, c := range cycles(next) {
		for _, id := range c {
			p.add(b.redirects[id].file, "/to", "redirect loop: %s", cycleText(c))
		}
	}
}

// inSitePath returns the path in the site, with the query, that to, the target of a redirect, sends requests to: to
// without its fragment if it is a path, or the path of an http or https URL in the site. The IDs of entities are not
// followed: their URLs have no query, so a redirect that matched one would have the path of an output, a duplicate
// (4.1).
func (b *builder) inSitePath(to string) (string, bool) {
	if strings.HasPrefix(to, "/") {
		path, _, _ := strings.Cut(to, "#")
		return path, true
	}
	return content.SitePath(b.site.URL, to)
}

// claim records v, such as the name of a file, as the first with key in firsts, or returns the one recorded for key
// and true.
func claim[K comparable, V any](firsts map[K]V, key K, v V) (first V, taken bool) {
	if first, taken = firsts[key]; !taken {
		firsts[key] = v
	}
	return first, taken
}

// cycles returns the cycles in next, which maps a node to the next one, if any, such as a page to its parent. Each
// cycle starts at its smallest node and follows next; the cycles are in the order of their first nodes.
func cycles(next map[string]string) [][]string {
	const (
		unseen = iota
		onPath
		done
	)
	state := make(map[string]int, len(next))
	var found [][]string
	for _, start := range slices.Sorted(maps.Keys(next)) {
		var path []string
		n, ok := start, true
		for ok && state[n] == unseen {
			state[n] = onPath
			path = append(path, n)
			n, ok = next[n]
		}
		if ok && state[n] == onPath {
			c := path[slices.Index(path, n):]
			i := slices.Index(c, slices.Min(c))
			found = append(found, slices.Concat(c[i:], c[:i]))
		}
		for _, m := range path {
			state[m] = done
		}
	}
	slices.SortFunc(found, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	return found
}

// cycleText returns c, a cycle from cycles, as a text such as "A -> B -> A".
func cycleText(c []string) string {
	return strings.Join(slices.Concat(c, c[:1]), " -> ")
}

// trimSlash removes a trailing "/" from path, unless path is "/", as Redirect.Matches compares paths.
func trimSlash(path string) string {
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
	return trimSlash(path), params
}

// fromKey returns s as splitFrom does, in one string. Two froms have the same key if and only if they match the same
// requests; a from with a query never has the key of one without.
func fromKey(s string) string {
	path, params := splitFrom(s)
	if len(params) == 0 {
		return path
	}
	return path + "?" + strings.Join(params, "&")
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

// redirectIndex finds the redirect that applies to a request.
type redirectIndex struct {
	redirects map[string]*doc[content.Redirect]
	ids       map[string][]string // in ID order, by the path of splitFrom and the first query parameter: "/a?" or "/a?x=1"
}

func newRedirectIndex(redirects map[string]*doc[content.Redirect]) redirectIndex {
	x := redirectIndex{redirects: redirects, ids: make(map[string][]string)}
	for _, id := range slices.Sorted(maps.Keys(redirects)) {
		path, params := splitFrom(redirects[id].v.From)
		k := path + "?"
		if len(params) > 0 {
			k += params[0]
		}
		x.ids[k] = append(x.ids[k], id)
	}
	return x
}

// match returns the ID of the redirect that applies to target, a path in the site with an optional query and no
// fragment, or "" if none does: of the redirects that match target, the first by comparePrecedence, and then by ID.
// Only the redirects whose froms have no query or a first parameter that target has can match it.
func (x redirectIndex) match(target string) string {
	path, params := splitFrom(target)
	best := ""
	for _, param := range slices.Concat([]string{""}, params) {
		for _, id := range x.ids[path+"?"+param] {
			r := x.redirects[id].v
			if r.Matches(target) && (best == "" || comparePrecedence(r.From, x.redirects[best].v.From) < 0) {
				best = id
			}
		}
	}
	return best
}
