package siteplan

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// ListKey is a list of entries in a language: the latest posts (theme.KindPosts), or the entries of a term
// (KindTerm), of a type (KindArchive) or of an author (KindAuthor).
type ListKey struct {
	Kind     theme.Kind
	Language string
	Of       string // the ID of the term or of the author, or the name of the type; "" for the latest posts
}

// Lists holds the published entries of the lists by ListKey.
type Lists map[ListKey][]*Entry

// Sorted returns the entries of the list ListKey{kind, language, of}, sorted in place by SortEntries, with the sticky
// posts first in the latest posts.
func (l Lists) Sorted(kind theme.Kind, language, of string) []*Entry {
	es := l[ListKey{kind, language, of}]
	SortEntries(es, kind == theme.KindPosts)
	return es
}

// Members returns the published entries of every list, unordered: the posts of each language; the entries with each
// term or, in a hierarchical taxonomy, a descendant of it, each entry once; the entries of each type that has a list;
// and the posts of each author in each language. It needs s.Site.
func (s *Site) Members() Lists {
	archives := make(map[string]bool)
	for _, t := range s.Site.Types {
		archives[t.Name] = t.Archive != ""
	}
	m := make(Lists)
	add := func(k ListKey, d *Entry) { m[k] = append(m[k], d) }
	for _, id := range slices.Sorted(maps.Keys(s.Entries)) {
		d := s.Published(id)
		if d == nil {
			continue
		}
		lang := d.E.Language
		if d.E.Type == content.TypePost {
			add(ListKey{theme.KindPosts, lang, ""}, d)
			if d.Rev.Author != "" {
				add(ListKey{theme.KindAuthor, lang, d.Rev.Author}, d)
			}
		}
		if archives[d.E.Type] {
			add(ListKey{theme.KindArchive, lang, d.E.Type}, d)
		}
		var terms []string
		for _, ids := range d.Rev.Terms {
			for _, t := range ids {
				// Only the terms of hierarchical taxonomies have parents.
				for ; t != "" && !slices.Contains(terms, t); t = s.Terms[t].V.Parent {
					terms = append(terms, t)
				}
			}
		}
		for _, t := range terms {
			add(ListKey{theme.KindTerm, lang, t}, d)
		}
	}
	return m
}

// SortEntries sorts es, published entries, as lists show them: newest first, by the date of a post or the time when
// another entry was created, compared as instants; then by number, the larger first; then by ID. With sticky, the
// sticky posts come first, each group in that order.
func SortEntries(es []*Entry, sticky bool) {
	type keyed struct {
		d *Entry
		t time.Time
	}
	ks := make([]keyed, len(es))
	for i, d := range es {
		ks[i] = keyed{d, ListTime(d)}
	}
	slices.SortFunc(ks, func(a, b keyed) int {
		if sticky && a.d.Rev.Sticky != b.d.Rev.Sticky {
			if a.d.Rev.Sticky {
				return -1
			}
			return 1
		}
		return cmp.Or(b.t.Compare(a.t), cmp.Compare(b.d.E.Number, a.d.E.Number), strings.Compare(a.d.E.ID, b.d.E.ID))
	})
	for i, k := range ks {
		es[i] = k.d
	}
}

// ListTime returns the time by which lists order d, a published entry: the date of a post, or the time when another
// entry was created.
func ListTime(d *Entry) time.Time {
	s := d.E.CreatedAt
	if d.E.Type == content.TypePost {
		s = d.Rev.Date
	}
	// Both are validated as RFC 3339.
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// PostsPerPage returns the number of entries on each page of a list. It needs s.Site.
func (s *Site) PostsPerPage() int {
	return cmp.Or(s.Site.PostsPerPage, content.DefaultPostsPerPage)
}
