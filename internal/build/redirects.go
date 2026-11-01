package build

import (
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// redirect is a redirect that the servers serve: one of the site with its target resolved, or one that the build
// generates.
type redirect struct {
	from      string // in the canonical form of froms, from the site URL, such as "/?p=123"
	to        string // a path from the site URL, such as "/news/" (as written if To is a path), or an absolute URL
	abs       bool   // to is an absolute URL
	status    int
	src       source // the from of the redirect, or the item of the settings that generates it
	generated bool
}

// resolveRedirects sets b.serverRedirects to the redirects of the site with their targets resolved (6.6.6), and those
// that the build generates for the front pages (frontRedirects) whose froms neither those redirects nor the outputs
// have (as fromKey compares them), sorted by from. The ID of an entity becomes the path of its page: that of an entry
// (the front page at the path of its language, the posts page at its list), the list of a term, or the author page in
// the default language; or the path of the published file of a medium, which mediaPath returns. A redirect to an entry
// that is not published is left out, with a warning. The errors, such as those of mediaPath, go to p.
func (b *builder) resolveRedirects(mediaPath func(id string) (string, error), p *problems) {
	var rs []redirect
	for _, id := range slices.Sorted(maps.Keys(b.redirects)) {
		d := b.redirects[id]
		src := source{file: d.file, pointer: "/from", what: d.file.Name}
		r := redirect{from: d.v.From, to: d.v.To, status: d.v.Status, src: src}
		switch to := d.v.To; {
		case strings.HasPrefix(to, "/"):
		case strings.Contains(to, ":"):
			r.abs = true
		case b.entries[to] != nil:
			path, ok := b.entryPaths[to]
			if !ok {
				b.warnings.add(d.file, "/to", "entry %q is not published; the redirect is left out", to)
				continue
			}
			r.to = path
		case b.terms[to] != nil:
			r.to = b.termPaths[to]
		case b.authors[to] != nil:
			r.to = b.authorPaths[to][b.site.DefaultLanguage]
		default: // a medium, the only other target that validation accepts
			path, err := mediaPath(to)
			if err != nil {
				p.add(d.file, "/to", "cannot make the path: %v", err)
				continue
			}
			r.to = path
		}
		rs = append(rs, r)
	}
	taken := make(map[string]bool)
	for _, r := range rs {
		taken[fromKey(r.from)] = true
	}
	for _, o := range b.outputs {
		taken[fromKey(o.path)] = true
	}
	for _, r := range b.frontRedirects(p) {
		if k := fromKey(r.from); !taken[k] {
			taken[k] = true
			rs = append(rs, r)
		}
	}
	slices.SortStableFunc(rs, func(x, y redirect) int { return strings.Compare(x.from, y.from) })
	b.serverRedirects = rs
}

// frontRedirects returns the redirects that the build generates to the front pages: with prefixDefaultLanguage, from
// "/" to the path of the default language with 302, so that how a language is chosen there can change; and from the
// path of each frontPage by its URL rule, such as "/home/", to the path of its language with 301. The errors of the
// paths go to p.
func (b *builder) frontRedirects(p *problems) []redirect {
	var rs []redirect
	add := func(from, lang, pointer string, status int) {
		src := source{file: b.settings, pointer: pointer, what: "the redirect to the front page in " + lang}
		rs = append(rs, redirect{from: from, to: b.site.LanguagePath(lang), status: status, src: src, generated: true})
	}
	if b.site.PrefixDefaultLanguage {
		add("/", b.site.DefaultLanguage, "/prefixDefaultLanguage", 302)
	}
	for _, lang := range b.site.Languages {
		d := b.published(b.site.FrontPage[lang])
		if d == nil {
			continue
		}
		path, err := b.site.EntryRulePath(&d.e, d.rev, b.pathRef)
		if err != nil {
			p.add(d.revFile, "/slug", "cannot make the path: %v", err)
			continue
		}
		add(content.CanonicalFrom(path), lang, "/frontPage/"+lang, 301)
	}
	return rs
}
