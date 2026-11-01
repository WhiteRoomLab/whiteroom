package build

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// listKey is a list of entries in a language: the latest posts (theme.KindPosts), or the entries of a term
// (KindTerm), of a type (KindArchive) or of an author (KindAuthor).
type listKey struct {
	kind     theme.Kind
	language string
	of       string // the ID of the term or of the author, or the name of the type; "" for the latest posts
}

// lists holds the published entries of the lists by listKey.
type lists map[listKey][]*entryDoc

// sorted returns the entries of the list listKey{kind, language, of}, sorted in place by sortEntries, with the sticky
// posts first in the latest posts.
func (l lists) sorted(kind theme.Kind, language, of string) []*entryDoc {
	es := l[listKey{kind, language, of}]
	sortEntries(es, kind == theme.KindPosts)
	return es
}

// members returns the published entries of every list, unordered: the posts of each language; the entries with each
// term or, in a hierarchical taxonomy, a descendant of it, each entry once; the entries of each type that has a list;
// and the posts of each author in each language.
func (b *builder) members() lists {
	archives := make(map[string]bool)
	for _, t := range b.site.Types {
		archives[t.Name] = t.Archive != ""
	}
	m := make(lists)
	add := func(k listKey, d *entryDoc) { m[k] = append(m[k], d) }
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		d := b.published(id)
		if d == nil {
			continue
		}
		lang := d.e.Language
		if d.e.Type == content.TypePost {
			add(listKey{theme.KindPosts, lang, ""}, d)
			if d.rev.Author != "" {
				add(listKey{theme.KindAuthor, lang, d.rev.Author}, d)
			}
		}
		if archives[d.e.Type] {
			add(listKey{theme.KindArchive, lang, d.e.Type}, d)
		}
		var terms []string
		for _, ids := range d.rev.Terms {
			for _, t := range ids {
				// Only the terms of hierarchical taxonomies have parents.
				for ; t != "" && !slices.Contains(terms, t); t = b.terms[t].v.Parent {
					terms = append(terms, t)
				}
			}
		}
		for _, t := range terms {
			add(listKey{theme.KindTerm, lang, t}, d)
		}
	}
	return m
}

// sortEntries sorts es, published entries, as lists show them: newest first, by the date of a post or the time when
// another entry was created, compared as instants; then by number, the larger first; then by ID. With sticky, the
// sticky posts come first, each group in that order.
func sortEntries(es []*entryDoc, sticky bool) {
	type keyed struct {
		d *entryDoc
		t time.Time
	}
	ks := make([]keyed, len(es))
	for i, d := range es {
		ks[i] = keyed{d, listTime(d)}
	}
	slices.SortFunc(ks, func(a, b keyed) int {
		if sticky && a.d.rev.Sticky != b.d.rev.Sticky {
			if a.d.rev.Sticky {
				return -1
			}
			return 1
		}
		return cmp.Or(b.t.Compare(a.t), cmp.Compare(b.d.e.Number, a.d.e.Number), strings.Compare(a.d.e.ID, b.d.e.ID))
	})
	for i, k := range ks {
		es[i] = k.d
	}
}

// listTime returns the time by which lists order d, a published entry: the date of a post, or the time when another
// entry was created.
func listTime(d *entryDoc) time.Time {
	s := d.e.CreatedAt
	if d.e.Type == content.TypePost {
		s = d.rev.Date
	}
	// Both are validated as RFC 3339.
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// addList adds the pages of a list with the entries es, in order: first, page 1 with the kind, the language, the path
// and what the list is of, and from page 2 on, a page at content.PagedPath for each postsPerPage entries. A list
// without entries has page 1 alone. It reports at src why a page has no file, and whether every page has one.
func (b *builder) addList(p *problems, first page, es []*entryDoc, src source, rank int) bool {
	per := b.postsPerPage()
	total := max(1, (len(es)+per-1)/per)
	for n := 1; n <= total; n++ {
		pg := first
		pg.path, pg.number, pg.total, pg.first = content.PagedPath(first.path, n), n, total, first.path
		lo, hi := min((n-1)*per, len(es)), min(n*per, len(es))
		pg.entries = es[lo:hi:hi]
		if !b.addPage(p, &pg, src, rank) {
			return false
		}
	}
	return true
}

// postsPerPage returns the number of entries on each page of a list.
func (b *builder) postsPerPage() int {
	return cmp.Or(b.site.PostsPerPage, content.DefaultPostsPerPage)
}
