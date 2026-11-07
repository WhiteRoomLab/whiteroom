package siteplan

import (
	"maps"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// TestMembersMissingTerms checks that the lists skip the terms that are not in Site.Terms, as a site made from the
// summaries of a store can have them: a term that a published revision names, and a parent of a term.
func TestMembersMissingTerms(t *testing.T) {
	const (
		post    = "ent_01JF0000000000000000000003"
		present = "trm_01JF0000000000000000000001"
		orphan  = "trm_01JF0000000000000000000002" // its parent is missing
		missing = "trm_01JF0000000000000000000009"
		child   = "trm_01JF0000000000000000000004" // its parent is orphan
	)
	term := func(id, taxonomy, parent string) *Doc[content.Term] {
		return &Doc[content.Term]{
			File: content.SiteFile{Name: "terms/" + id + ".json"},
			V: content.Term{
				ID: id, Taxonomy: taxonomy, Language: "ja", TranslationGroup: "tg_" + id[4:], Slug: "t-" + id[len(id)-1:],
				Parent: parent,
			},
		}
	}
	s := smallSite(t, "a", "b")
	s.Entries[post] = &Entry{
		File: content.SiteFile{Name: "entries/" + post + ".json"},
		E: content.Entry{
			ID: post, Number: 3, Type: content.TypePost, Language: "ja", TranslationGroup: "tg_" + post[4:],
			Status: content.StatusPublished, PublishedRevision: "rev_" + post[4:], CreatedAt: "2026-10-25T10:00:00+09:00",
		},
		RevFile: content.SiteFile{Name: "revisions/rev_" + post[4:] + ".json"},
		Rev: &content.Revision{
			Title: "p", Slug: "p", Date: "2026-10-25T10:00:00+09:00",
			Terms: content.Terms{content.TaxonomyCategory: {missing, present, child}, content.TaxonomyTag: {missing}},
		},
	}
	s.Terms = map[string]*Doc[content.Term]{
		present: term(present, content.TaxonomyCategory, ""),
		orphan:  term(orphan, content.TaxonomyCategory, missing),
		child:   term(child, content.TaxonomyCategory, orphan),
	}

	got := s.Members()
	want := Lists{
		{theme.KindPosts, "ja", ""}:     {s.Entries[post]},
		{theme.KindTerm, "ja", present}: {s.Entries[post]},
		{theme.KindTerm, "ja", child}:   {s.Entries[post]},
		{theme.KindTerm, "ja", orphan}:  {s.Entries[post]},
	}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Members() = %v, want the lists of the terms that are present: %v", got, want)
	}

	// Plan, which uses the lists, does not panic either; it reports the paths of the terms that it cannot make.
	notFound := `: /slug: cannot make the path: term "` + missing + `" is not found`
	wantProblems := []string{"terms/" + orphan + ".json" + notFound, "terms/" + child + ".json" + notFound}
	if problems := reported(func(report Report) { s.Plan(report) }); !slices.Equal(problems, wantProblems) {
		t.Errorf("Plan() reported %q, want %q", problems, wantProblems)
	}
}
