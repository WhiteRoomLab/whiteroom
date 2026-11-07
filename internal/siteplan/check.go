package siteplan

import (
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// CheckAcross checks what the documents must agree on, the checks across entities of 6.7, reporting the problems.
// Where two documents conflict, the later one in ID order gets the error, which names the file of the first one. It
// needs s.Site.
func (s *Site) CheckAcross(report Report) {
	s.checkFrontPages(report)
	s.checkSlugs(report)
	s.checkTranslationGroups(report)
	s.checkMenuLocations(report)
	s.checkParents(report)
	s.checkRedirects(report)
}

// checkFrontPages checks that the front pages and the posts pages of the settings are published.
func (s *Site) checkFrontPages(report Report) {
	for _, pages := range []struct {
		pointer string
		ids     content.Localized
	}{{"/frontPage", s.Site.FrontPage}, {"/postsPage", s.Site.PostsPage}} {
		for _, l := range s.Site.Languages {
			if id := pages.ids[l]; id != "" && s.Published(id) == nil {
				report.addf(s.Settings, pages.pointer+"/"+l, "entry %q is not published", id)
			}
		}
	}
}

// checkSlugs checks that the slugs of the published entries are unique in their languages and types, and those of
// pages under their parents (6.4.2); those of the terms in their taxonomies and languages; and those of the authors.
func (s *Site) checkSlugs(report Report) {
	type entryKey struct{ language, typ, parent, slug string }
	entries := make(map[entryKey]string)
	for _, id := range slices.Sorted(maps.Keys(s.Entries)) {
		d := s.Published(id)
		if d == nil {
			continue
		}
		k := entryKey{language: d.E.Language, typ: d.E.Type, slug: d.Rev.Slug}
		if d.E.Type == content.TypePage {
			k.parent = d.Rev.Parent
		}
		if first, taken := claim(entries, k, d.RevFile.Name); taken {
			report.addf(d.RevFile, "/slug", "slug %q is also used by %s", d.Rev.Slug, first)
		}
	}
	type termKey struct{ taxonomy, language, slug string }
	terms := make(map[termKey]string)
	for _, id := range slices.Sorted(maps.Keys(s.Terms)) {
		d := s.Terms[id]
		if first, taken := claim(terms, termKey{d.V.Taxonomy, d.V.Language, d.V.Slug}, d.File.Name); taken {
			report.addf(d.File, "/slug", "slug %q is also used by %s", d.V.Slug, first)
		}
	}
	authors := make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(s.Authors)) {
		d := s.Authors[id]
		if first, taken := claim(authors, d.V.Slug, d.File.Name); taken {
			report.addf(d.File, "/slug", "slug %q is also used by %s", d.V.Slug, first)
		}
	}
}

// checkTranslationGroups checks that a translation group has at most one entry, of any status, and one term in each
// language. Entries without a group or a language, which drafts may be, are left out.
func (s *Site) checkTranslationGroups(report Report) {
	type key struct{ group, language string }
	entries := make(map[key]string)
	for _, id := range slices.Sorted(maps.Keys(s.Entries)) {
		d := s.Entries[id]
		k := key{d.E.TranslationGroup, d.E.Language}
		if k.group == "" || k.language == "" {
			continue
		}
		if first, taken := claim(entries, k, d.File.Name); taken {
			report.addf(d.File, "/translationGroup", "translation group %q already has an entry in the language %q: %s",
				k.group, k.language, first)
		}
	}
	terms := make(map[key]string)
	for _, id := range slices.Sorted(maps.Keys(s.Terms)) {
		d := s.Terms[id]
		k := key{d.V.TranslationGroup, d.V.Language}
		if first, taken := claim(terms, k, d.File.Name); taken {
			report.addf(d.File, "/translationGroup", "translation group %q already has a term in the language %q: %s",
				k.group, k.language, first)
		}
	}
}

// checkMenuLocations checks that each language has at most one menu at each location.
func (s *Site) checkMenuLocations(report Report) {
	type key struct{ language, location string }
	menus := make(map[key]string)
	for _, id := range slices.Sorted(maps.Keys(s.Menus)) {
		d := s.Menus[id]
		if d.V.Location == "" {
			continue
		}
		if first, taken := claim(menus, key{d.V.Language, d.V.Location}, d.File.Name); taken {
			report.addf(d.File, "/location", "location %q already has a menu in the language %q: %s",
				d.V.Location, d.V.Language, first)
		}
	}
}

// checkParents checks that the parents of the published pages are published, and that the parents of neither the
// pages nor the terms make cycles. A page or a term that leads to a cycle without being in it gets no error.
func (s *Site) checkParents(report Report) {
	pages := make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(s.Entries)) {
		d := s.Published(id)
		if d == nil || d.E.Type != content.TypePage || d.Rev.Parent == "" {
			continue
		}
		if s.Published(d.Rev.Parent) == nil {
			report.addf(d.RevFile, "/parent", "entry %q is not published", d.Rev.Parent)
		} else {
			pages[id] = d.Rev.Parent
		}
	}
	for _, c := range cycles(pages) {
		for _, id := range c {
			report.addf(s.Entries[id].RevFile, "/parent", "the parents make a cycle: %s", cycleText(c))
		}
	}
	terms := make(map[string]string)
	for id, d := range s.Terms {
		if d.V.Parent != "" {
			terms[id] = d.V.Parent
		}
	}
	for _, c := range cycles(terms) {
		for _, id := range c {
			report.addf(s.Terms[id].File, "/parent", "the parents make a cycle: %s", cycleText(c))
		}
	}
}

// checkRedirects checks that no two redirects match the same requests, and that following redirects from one never
// leads back to it (6.6.6). A chain of redirects that ends is allowed.
func (s *Site) checkRedirects(report Report) {
	keys := make(map[string]string)
	for _, id := range slices.Sorted(maps.Keys(s.Redirects)) {
		d := s.Redirects[id]
		if first, taken := claim(keys, FromKey(d.V.From), d.File.Name); taken {
			report.addf(d.File, "/from", "from %q matches the same requests as that of %s", d.V.From, first)
		}
	}
	index := newRedirectIndex(s.Redirects)
	next := make(map[string]string)
	for id, d := range s.Redirects {
		if target, ok := s.inSitePath(d.V.To); ok {
			if n := index.match(target); n != "" {
				next[id] = n
			}
		}
	}
	for _, c := range cycles(next) {
		for _, id := range c {
			report.addf(s.Redirects[id].File, "/to", "redirect loop: %s", cycleText(c))
		}
	}
}

// inSitePath returns the path in the site, with the query, that to, the target of a redirect, sends requests to: to
// without its fragment if it is a path, or the path of an http or https URL in the site. The IDs of entities are not
// followed: their URLs have no query, so a redirect that matched one would have the path of an output, a duplicate
// (4.1).
func (s *Site) inSitePath(to string) (string, bool) {
	if strings.HasPrefix(to, "/") {
		path, _, _ := strings.Cut(to, "#")
		return path, true
	}
	return content.SitePath(s.Site.URL, to)
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
