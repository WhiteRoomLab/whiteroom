package build

import (
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// TestListOrder checks the members of the lists and their order: newest first by the date of a post, compared as an
// instant, or by the time when another entry was created; then by number and by ID; with the sticky posts first on the
// lists of the latest posts only.
func TestListOrder(t *testing.T) {
	src := testSource(t)
	src.schema.Data = []byte(strings.Replace(testSchema, "    url: /{yyyy}/{mm}/{dd}/{slug}/\n",
		"    url: /{yyyy}/{mm}/{dd}/{slug}/\n    taxonomies: [works_category]\n", 1))
	// post adds a post by the author of 6.11.4 with the ID newID("ent", n).
	post := func(n, number int, language, date string, sticky bool, terms content.Terms) string {
		id := newID("ent", n)
		src.putEntry(t, newEntry(id, number, content.TypePost, language, groupOf(id)), content.Revision{
			Title:  fmt.Sprint("Post ", n),
			Slug:   fmt.Sprint("post-", n),
			Date:   date,
			Author: authorID,
			Sticky: sticky,
			Terms:  terms,
		})
		return id
	}
	// works adds a works entry created at createdAt with the ID newID("ent", n).
	works := func(n, number int, createdAt string) string {
		id := newID("ent", n)
		e := newEntry(id, number, "works", "ja", groupOf(id))
		e.CreatedAt = createdAt
		src.putEntry(t, e, content.Revision{
			Title:  fmt.Sprint("Works ", n),
			Slug:   fmt.Sprint("works-", n),
			Terms:  content.Terms{"works_category": {worksCategoryID}},
			Fields: content.Fields{"client": jsontext.Value(`"〇〇株式会社"`)},
		})
		return id
	}
	events := content.Terms{content.TaxonomyTag: {enEventTagID}}
	// enPostID has the date 2026-10-26T00:30:00+09:00 and the number 124.
	newest := post(1, 2, "en", "2026-10-25T16:00:00Z", false, events)
	sameUTC := post(2, 200, "en", "2026-10-25T15:30:00Z", false, events)
	sameNumber := post(3, 200, "en", "2026-10-25T06:30:00-09:00", false, events)
	sameOffset := post(4, 1, "en", "2026-10-26T00:30:00+09:00", false, events)
	stickyEn := post(5, 3, "en", "2026-01-01T00:00:00Z", true, events)
	// postID has the date 2026-10-26T00:30:00+09:00, the category press-release, a child of news, and the tag イベント.
	stickyJa := post(6, 5, "ja", "2026-01-01T00:00:00+09:00", true,
		content.Terms{content.TaxonomyCategory: {newsCategoryID, pressReleaseID}})
	// Created on 2026-10-25, after the works entries, but dated before worksID.
	dated := post(7, 12, "ja", "2026-10-19T12:00:00Z", false, content.Terms{"works_category": {worksCategoryID}})
	// worksID was created at 2026-10-20T10:00:00+09:00.
	laterWorks := works(8, 10, "2026-10-20T01:30:00Z")
	earlierWorks := works(9, 11, "2026-10-19T00:00:00Z")
	b := mustLoad(t, src)

	enPosts := []string{newest, sameUTC, sameNumber, enPostID, sameOffset, stickyEn}
	for _, tt := range []struct {
		path string
		want []string
	}{
		{"/en/", append([]string{stickyEn}, enPosts[:5]...)},
		{"/en/author/yamada/", enPosts},
		{"/en/tag/events/", enPosts},
		{"/news/", []string{stickyJa, postID, dated}},
		{"/author/yamada/", []string{postID, dated, stickyJa}},
		{"/category/news/", []string{postID, stickyJa}},
		{"/category/news/press-release/", []string{postID, stickyJa}},
		{"/tag/イベント/", []string{postID}},
		{"/works/", []string{laterWorks, worksID, earlierWorks}},
		{"/works_category/corporate-site/", []string{laterWorks, worksID, dated, earlierWorks}},
		{"/en/works/", []string{}},
	} {
		pg := pageAt(b, tt.path)
		if pg == nil {
			t.Errorf("no page at %q", tt.path)
			continue
		}
		if got := entryIDs(pg.entries); !slices.Equal(got, tt.want) || pg.total != 1 {
			t.Errorf("list at %q (%d pages) = %q, want %q", tt.path, pg.total, got, tt.want)
		}
	}
}

// TestListPages checks that a list has a page for each postsPerPage entries, in order, and page 1 without entries.
func TestListPages(t *testing.T) {
	for _, perPage := range []int{0, 1, 3, 5, 11, 12, 13, 100} {
		t.Run(fmt.Sprint(perPage), func(t *testing.T) {
			src := testSource(t)
			want := newestFirst(postID, src.addPosts(t, "ja", 11))
			site := testSite()
			site.PostsPerPage = perPage
			src.putSettings(t, site)
			b := mustLoad(t, src)
			per := perPage
			if per == 0 {
				per = content.DefaultPostsPerPage
			}
			total := (len(want) + per - 1) / per
			var got []string
			for n := 1; n <= total; n++ {
				path := content.PagedPath("/news/", n)
				pg := pageAt(b, path)
				if pg == nil {
					t.Fatalf("no page at %q", path)
				}
				size := min(per, len(want)-(n-1)*per)
				if pg.number != n || pg.total != total || pg.first != "/news/" || len(pg.entries) != size {
					t.Errorf("page at %q is %d/%d of %q with %d entries, want %d/%d of \"/news/\" with %d",
						path, pg.number, pg.total, pg.first, len(pg.entries), n, total, size)
				}
				got = append(got, entryIDs(pg.entries)...)
			}
			if !slices.Equal(got, want) {
				t.Errorf("entries on the pages = %q, want %q", got, want)
			}
			if path := content.PagedPath("/news/", total+1); pageAt(b, path) != nil {
				t.Errorf("page at %q", path)
			}
		})
	}
}

// TestListEmpty checks that the lists without entries have page 1 alone.
func TestListEmpty(t *testing.T) {
	src := testSource(t)
	delete(src.docs[content.RefEntry], enPostID)
	x := newTerm(1, content.TaxonomyTag, "ja", "empty")
	src.put(t, content.RefTerm, x.ID, x)
	b := mustLoad(t, src)
	for _, tt := range []struct {
		path string
		kind theme.Kind
	}{
		{"/tag/empty/", theme.KindTerm},
		{"/en/tag/events/", theme.KindTerm},
		{"/en/", theme.KindPosts},
		{"/en/works/", theme.KindArchive},
		{"/en/author/yamada/", theme.KindAuthor},
	} {
		pg := pageAt(b, tt.path)
		if pg == nil || pg.kind != tt.kind || pg.number != 1 || pg.total != 1 || pg.first != tt.path || len(pg.entries) != 0 {
			t.Errorf("page at %q = %+v, want page 1 of 1 without entries", tt.path, pg)
		}
		if path := content.PagedPath(tt.path, 2); pageAt(b, path) != nil {
			t.Errorf("page at %q", path)
		}
	}
}

// TestPlanDeterministic checks that loading a site again plans the same pages and outputs in the same order.
func TestPlanDeterministic(t *testing.T) {
	src := testSource(t)
	src.addPosts(t, "ja", 11)
	src.addPosts(t, "en", 11)
	// summary returns what b has planned as text.
	summary := func(b *builder) string {
		var s strings.Builder
		for _, pg := range b.pages {
			var entry, term, author string
			if pg.entry != nil {
				entry = pg.entry.e.ID
			}
			if pg.term != nil {
				term = pg.term.v.ID
			}
			if pg.author != nil {
				author = pg.author.v.ID
			}
			fmt.Fprintf(&s, "%d %s %s %s %d/%d %s %t %s %s %s %s %q\n", pg.kind, pg.language, pg.path, pg.file, pg.number,
				pg.total, pg.first, pg.front, entry, term, author, pg.typeName, entryIDs(pg.entries))
		}
		for _, o := range b.outputs {
			fmt.Fprintf(&s, "%s %s %s %s %q %d\n", o.path, o.file, o.src.file.Name, o.src.pointer, o.src.what, o.rank)
		}
		fmt.Fprintln(&s, b.base, b.entryPaths, b.termPaths, b.authorPaths)
		return s.String()
	}
	want := summary(mustLoad(t, src))
	for range 5 {
		if got := summary(mustLoad(t, src)); got != want {
			t.Fatalf("plan() differs between loads:\n%s\nand:\n%s", got, want)
		}
	}
}
