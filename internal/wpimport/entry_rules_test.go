package wpimport

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// rulesPage is a page of the WXR of the tests of the rules across entries, whose parent is the item with the post ID parent
// (0 for none). The status "" is publish.
func rulesPage(id int, name string, parent int, status string) wxrEntry {
	return wxrEntry{id: id, typ: "page", status: status, title: fmt.Sprintf("Page %d", id), name: name, parent: parent}
}

// rulesPost is a post of that WXR.
func rulesPost(id int, name, status string) wxrEntry {
	return wxrEntry{id: id, status: status, title: fmt.Sprintf("Post %d", id), name: name}
}

// rulesSettings is entrySettings with the front page and the posts page, which are the IDs in WordPress (0 for none).
func rulesSettings(front, posts int64) *Settings {
	s := entrySettings()
	s.Site.FrontPage, s.Site.PostsPage = front, posts
	return s
}

// parentPost returns the post ID of the parent of the entry d, 0 if it has none, and -1 if the parent is not among docs.
func parentPost(docs []*EntryDoc, d *EntryDoc) int64 {
	if d.Revision.Parent == "" {
		return 0
	}
	for _, o := range docs {
		if o.Entry.ID == d.Revision.Parent {
			return o.Post
		}
	}
	return -1
}

// pointerLines returns the lines of the report about the item post that point at pointer of its revision or its entry.
func pointerLines(imp *Import, post int64, pointer string) []string {
	var out []string
	for _, l := range itemLines(imp.Report, post) {
		if strings.HasSuffix(l, "#"+pointer) {
			out = append(out, l)
		}
	}
	return out
}

// expandFiles replaces {E} and {R} in the lines with the files of the entry of the item post, if there is one.
func expandFiles(docs []*EntryDoc, post int64, lines []string) []string {
	d := docOf(docs, post)
	if d == nil {
		return lines
	}
	files := strings.NewReplacer("{E}", "entries/"+d.Entry.ID+".json", "{R}", "revisions/"+d.RevisionID+".json")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = files.Replace(l)
	}
	return out
}

// lowestEntry returns the post of the item of posts whose entry has the lowest ID.
func lowestEntry(t *testing.T, imp *Import, posts []int64) int64 {
	t.Helper()
	lowest := posts[0]
	for _, p := range posts[1:] {
		if postIDOf(t, imp, p) < postIDOf(t, imp, lowest) {
			lowest = p
		}
	}
	return lowest
}

func TestEntriesParents(t *testing.T) {
	notInWXR := func(n int) string {
		return fmt.Sprintf("changed: the parent, item %d, is not in the WXR; the page is at the top -> {R}#/parent", n)
	}
	notImported := func(n int) string {
		return fmt.Sprintf("changed: the parent, item %d, is not imported; the page is at the top -> {R}#/parent", n)
	}
	notAPage := func(n int) string {
		return fmt.Sprintf("changed: the parent, item %d, is not a page; the page is at the top -> {R}#/parent", n)
	}
	// onlyPages is a group of the items of a type, which is not page, that have a parent: the first of them, and how many.
	type onlyPages struct {
		first int64
		typ   string
		items string
	}
	const (
		ownParent = "changed: the page is its own parent; it is at the top -> {R}#/parent"
		cycle     = "changed: the parents make a cycle; the page is at the top -> {R}#/parent"
	)
	// Eight pages lead to a cycle of two, the first of them the farthest, so a search that starts at it finds the cycle at the end
	// of a path of ten. The pages of the cycle have the highest IDs of the ten, so a cycle that took in the pages of the path
	// would lose the parent of one of those.
	var pool []wxrEntry
	for post := 1; post <= 10; post++ {
		pool = append(pool, rulesPage(post, fmt.Sprintf("p%d", post), 0, ""))
	}
	poolImp, _ := planEntries(t, nil, nil, entriesWXR("", pool...))
	ranked := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	slices.SortFunc(ranked, func(a, b int64) int { return strings.Compare(postIDOf(t, poolImp, a), postIDOf(t, poolImp, b)) })
	var longTail []wxrEntry
	longParents := make(map[int64]int64)
	for i, post := range ranked[:8] {
		parent := ranked[i+1]
		longTail = append(longTail, rulesPage(int(post), fmt.Sprintf("tail-%d", post), int(parent), ""))
		longParents[post] = parent
	}
	ring := ranked[8:]
	longTail = append(longTail, rulesPage(int(ring[0]), "ring-a", int(ring[1]), ""), rulesPage(int(ring[1]), "ring-b", int(ring[0]), ""))
	for _, tt := range []struct {
		name    string
		items   []wxrEntry
		parents map[int64]int64 // the post of the parent of each entry that has one in the end, outside the rings
		rings   [][]int64       // pages that have the next one for the parent, the last the first
		lines   map[int64][]string
		groups  []onlyPages
	}{
		{
			name:    "a page under a page",
			items:   []wxrEntry{rulesPage(1, "parent", 0, ""), rulesPage(2, "child", 1, "")},
			parents: map[int64]int64{2: 1},
		},
		{
			name:    "a grandchild",
			items:   []wxrEntry{rulesPage(1, "a", 0, ""), rulesPage(2, "b", 1, ""), rulesPage(3, "c", 2, "")},
			parents: map[int64]int64{2: 1, 3: 2},
		},
		{
			name:    "a child that comes before its parent",
			items:   []wxrEntry{rulesPage(3, "c", 2, ""), rulesPage(2, "b", 1, ""), rulesPage(1, "a", 0, "")},
			parents: map[int64]int64{3: 2, 2: 1},
		},
		{
			name:    "a draft under a draft",
			items:   []wxrEntry{rulesPage(1, "a", 0, "draft"), rulesPage(2, "b", 1, "draft")},
			parents: map[int64]int64{2: 1},
		},
		{
			name:  "a parent that is not in the WXR",
			items: []wxrEntry{rulesPage(2, "child", 99, "")},
			lines: map[int64][]string{2: {notInWXR(99)}},
		},
		{
			name:  "a parent in the trash",
			items: []wxrEntry{rulesPage(1, "a", 0, "trash"), rulesPage(2, "child", 1, "")},
			lines: map[int64][]string{1: {"skipped: the entry is not imported: it is in the trash"}, 2: {notImported(1)}},
		},
		{
			name:  "a parent that is an auto-draft",
			items: []wxrEntry{rulesPage(1, "a", 0, "auto-draft"), rulesPage(2, "child", 1, "")},
			lines: map[int64][]string{1: {"skipped: the entry is not imported: it is an auto-draft"}, 2: {notImported(1)}},
		},
		{
			name:  "an attachment as the parent",
			items: []wxrEntry{{id: 1, typ: "attachment", status: "inherit", title: "a.png", name: "a"}, rulesPage(2, "child", 1, "")},
			lines: map[int64][]string{2: {notImported(1)}},
		},
		{
			name:  "an item of a type that is not imported as the parent",
			items: []wxrEntry{{id: 1, typ: "product", title: "Mug", name: "mug"}, rulesPage(2, "child", 1, "")},
			lines: map[int64][]string{2: {notImported(1)}},
		},
		{
			name: "a parent whose dates cannot be read",
			items: []wxrEntry{
				{id: 1, typ: "page", title: "A", name: "a", date: zeroDate, dateGMT: zeroDate, modified: zeroDate, modifiedGMT: zeroDate},
				rulesPage(2, "child", 1, ""),
			},
			lines: map[int64][]string{
				1: {`skipped: the entry is not imported: its post_date "0000-00-00 00:00:00" and post_modified "0000-00-00 00:00:00" cannot be read`},
				2: {notImported(1)},
			},
		},
		{
			name:  "a post as the parent",
			items: []wxrEntry{rulesPost(1, "a", ""), rulesPage(2, "child", 1, "")},
			lines: map[int64][]string{2: {notAPage(1)}},
		},
		{
			name:  "an item of a custom type as the parent",
			items: []wxrEntry{{id: 1, typ: "works", title: "W", name: "w"}, rulesPage(2, "child", 1, "")},
			lines: map[int64][]string{2: {notAPage(1)}},
		},
		{
			name:  "the page itself as the parent",
			items: []wxrEntry{rulesPage(1, "a", 1, "")},
			lines: map[int64][]string{1: {ownParent}},
		},
		{
			name:    "a parent that is a page in the trash is not a reason for the child of a child",
			items:   []wxrEntry{rulesPage(1, "a", 0, "trash"), rulesPage(2, "b", 1, ""), rulesPage(3, "c", 2, "")},
			parents: map[int64]int64{3: 2},
			lines:   map[int64][]string{1: {"skipped: the entry is not imported: it is in the trash"}, 2: {notImported(1)}},
		},
		{
			name:  "two pages that are the parents of each other",
			items: []wxrEntry{rulesPage(1, "a", 2, ""), rulesPage(2, "b", 1, "")},
			rings: [][]int64{{1, 2}},
		},
		{
			name:  "three pages in a cycle",
			items: []wxrEntry{rulesPage(1, "a", 2, ""), rulesPage(2, "b", 3, ""), rulesPage(3, "c", 1, "")},
			rings: [][]int64{{1, 2, 3}},
		},
		{
			name:  "drafts in a cycle",
			items: []wxrEntry{rulesPage(1, "a", 2, "draft"), rulesPage(2, "b", 3, "draft"), rulesPage(3, "c", 1, "draft")},
			rings: [][]int64{{1, 2, 3}},
		},
		{
			// The pages that lead to the cycle keep their parents, whatever their IDs are.
			name: "a cycle with tails",
			items: []wxrEntry{
				rulesPage(4, "tail", 3, ""), rulesPage(3, "c", 1, ""), rulesPage(1, "a", 2, ""), rulesPage(2, "b", 3, ""),
				rulesPage(5, "tail-a", 1, ""), rulesPage(6, "tail-b", 2, ""), rulesPage(7, "tail-c", 4, ""), rulesPage(8, "tail-d", 7, ""),
				rulesPage(9, "tail-e", 3, ""), rulesPage(10, "tail-f", 9, ""),
			},
			parents: map[int64]int64{4: 3, 5: 1, 6: 2, 7: 4, 8: 7, 9: 3, 10: 9},
			rings:   [][]int64{{3, 1, 2}},
		},
		{
			name:    "a cycle with a long tail",
			items:   longTail,
			parents: longParents,
			rings:   [][]int64{ring},
		},
		{
			name: "two cycles",
			items: []wxrEntry{
				rulesPage(1, "a", 2, ""), rulesPage(2, "b", 1, ""), rulesPage(3, "c", 4, ""), rulesPage(4, "d", 5, ""), rulesPage(5, "e", 3, ""),
				rulesPage(6, "f", 5, ""),
			},
			parents: map[int64]int64{6: 5},
			rings:   [][]int64{{1, 2}, {3, 4, 5}},
		},
		{
			name:   "a post with a parent",
			items:  []wxrEntry{rulesPage(1, "a", 0, ""), {id: 2, title: "Post", name: "post", parent: 1}},
			groups: []onlyPages{{first: 2, typ: "post", items: "1 item"}},
		},
		{
			name:   "an item of a custom type with a parent that is not in the WXR",
			items:  []wxrEntry{{id: 2, typ: "works", title: "Work", name: "work", parent: 7}},
			groups: []onlyPages{{first: 2, typ: "works", items: "1 item"}},
		},
		{
			name:   "a draft post with a parent",
			items:  []wxrEntry{rulesPage(1, "a", 0, ""), {id: 2, title: "Post", name: "post", status: "draft", parent: 1}},
			groups: []onlyPages{{first: 2, typ: "post", items: "1 item"}},
		},
		{
			// One record for the items of a type, at the first of them. The pages are not counted.
			name: "items of two types with parents",
			items: []wxrEntry{
				rulesPage(1, "a", 0, ""), rulesPage(2, "b", 1, ""),
				{id: 3, typ: "works", title: "Work", name: "work"},
				{id: 4, typ: "works", title: "Work 2", name: "work-2", parent: 3},
				{id: 5, title: "Post", name: "post", parent: 4},
				{id: 6, typ: "works", title: "Work 3", name: "work-3", parent: 4},
				{id: 7, typ: "works", title: "Work 4", name: "work-4", parent: 7},
				{id: 8, title: "Post 2", name: "post-2", status: "trash", parent: 1},
			},
			parents: map[int64]int64{2: 1},
			lines:   map[int64][]string{8: {"skipped: the entry is not imported: it is in the trash"}},
			groups:  []onlyPages{{first: 4, typ: "works", items: "3 items"}, {first: 5, typ: "post", items: "1 item"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", tt.items...))
			want := make(map[int64]int64, len(tt.parents))
			for post, parent := range tt.parents {
				want[post] = parent
			}
			lines := make(map[int64][]string, len(tt.lines))
			for post, l := range tt.lines {
				lines[post] = l
			}
			for _, ring := range tt.rings {
				victim := lowestEntry(t, imp, ring)
				for i, post := range ring {
					if post != victim {
						want[post] = ring[(i+1)%len(ring)]
					}
				}
				lines[victim] = append(lines[victim], cycle)
			}
			for _, d := range docs {
				if got := parentPost(docs, d); got != want[d.Post] {
					t.Errorf("the parent of item %d is the item %d, want %d", d.Post, got, want[d.Post])
				}
			}
			for i := range imp.WXR.Items {
				post := imp.WXR.Items[i].ID
				// Only the lines about the parent, and those of the items that were left out.
				got := pointerLines(imp, post, "/parent")
				if docOf(docs, post) == nil {
					got = itemLines(imp.Report, post)
				}
				sameSet(t, fmt.Sprintf("the report about item %d", post), got, expandFiles(docs, post, lines[post]))
			}
			var groups []string
			for _, g := range tt.groups {
				groups = append(groups, groupLine(imp, g.first, g.typ,
					fmt.Sprintf("the parents of %s of the post type %s are not imported; only pages have parents", g.items, g.typ)))
			}
			sameSet(t, "the groups", refsGroupLines(t, imp), groups)
		})
	}
}

func TestEntriesParentsKeepTheStatus(t *testing.T) {
	// The parents of a page do not change its status, and a page at the top is as it was.
	in := entriesWXR("",
		rulesPage(1, "a", 0, ""), rulesPage(2, "b", 1, "future"), rulesPage(3, "c", 99, ""), rulesPage(4, "d", 4, "draft"),
	)
	_, _, docs := makeEntries(t, nil, nil, in)
	for post, want := range map[int64]content.Status{1: content.StatusPublished, 2: content.StatusScheduled, 3: content.StatusPublished, 4: content.StatusDraft} {
		if got := docOf(docs, post).Entry.Status; got != want {
			t.Errorf("the status of item %d is %q, want %q", post, got, want)
		}
	}
}

// rulesConv returns the conversion of the WXR in, which is where Entries is just before the rules across entries: each
// item has been through begin, convert and tidy, and its parents are those of the WXR, which setParents has not looked at.
func rulesConv(t *testing.T, in string) (*Import, *entryConv, []*entryItem) {
	t.Helper()
	imp, site := planEntries(t, nil, nil, in)
	c := &entryConv{imp: imp, site: site, opts: EntryOptions{Site: site, By: mediaTestBy}, zone: mustLocation(t, "Asia/Tokyo")}
	items, _, err := c.collect()
	if err != nil {
		t.Fatal(err)
	}
	var live []*entryItem
	for _, ei := range items {
		if c.begin(ei) {
			live = append(live, ei)
		}
	}
	for _, ei := range live {
		c.convert(ei)
		c.tidy(ei)
	}
	return imp, c, live
}

func TestEntriesParentOfAnotherLanguage(t *testing.T) {
	imp, c, items := rulesConv(t, entriesWXR("", rulesPage(1, "a", 0, ""), rulesPage(2, "b", 1, ""), rulesPage(3, "c", 2, "")))
	items[0].entry.Language = "en"
	c.setParents(items)
	want := "changed: the parent, page 1, is in another language; the page is at the top -> revisions/" + items[1].revID + ".json#/parent"
	if got := pointerLines(imp, 2, "/parent"); !slices.Equal(got, []string{want}) {
		t.Errorf("the report about item 2 is %q, want %q", got, want)
	}
	if items[1].rev.Parent != "" || items[2].rev.Parent != items[1].id {
		t.Errorf("the parents are %q and %q, want none and the page 2", items[1].rev.Parent, items[2].rev.Parent)
	}
}

func TestEntriesDraftUnderUnpublished(t *testing.T) {
	notPublished := func(n int) string {
		return fmt.Sprintf("review: the parent, page %d, is not published here; the page is a draft here -> {E}#/status", n)
	}
	const (
		pub, sched, draft = content.StatusPublished, content.StatusScheduled, content.StatusDraft
		private           = "review: the entry was private; it is a draft here -> {E}#/status"
	)
	parents := []struct {
		name string
		item wxrEntry
		want content.Status
	}{
		{"published", rulesPage(1, "parent", 0, ""), pub},
		{"scheduled", rulesPage(1, "parent", 0, "future"), sched},
		{"a draft", rulesPage(1, "parent", 0, "draft"), draft},
		{"pending", rulesPage(1, "parent", 0, "pending"), draft},
		{"private", rulesPage(1, "parent", 0, "private"), draft},
		{"protected by a password", wxrEntry{id: 1, typ: "page", title: "Parent", name: "parent", password: entryPassword}, draft},
		{"scheduled and protected by a password", wxrEntry{id: 1, typ: "page", status: "future", title: "Parent", name: "parent", password: entryPassword}, draft},
		{"without a title", wxrEntry{id: 1, typ: "page", name: "parent"}, draft},
		{"scheduled without a date", wxrEntry{id: 1, typ: "page", status: "future", title: "Parent", name: "parent", date: zeroDate, dateGMT: zeroDate}, draft},
	}
	children := []struct {
		name   string
		status string
		// want is the status of the child under a parent whose status is the key, and whether it has the record.
		want func(parent content.Status) (content.Status, bool)
	}{
		{"published", "", func(p content.Status) (content.Status, bool) {
			if p == pub {
				return pub, false
			}
			return draft, true
		}},
		{"scheduled", "future", func(p content.Status) (content.Status, bool) {
			if p == pub || p == sched {
				return sched, false
			}
			return draft, true
		}},
		{"a draft", "draft", func(content.Status) (content.Status, bool) { return draft, false }},
		{"private", "private", func(content.Status) (content.Status, bool) { return draft, false }},
	}
	for _, p := range parents {
		for _, ch := range children {
			t.Run(p.name+" parent, "+ch.name+" child", func(t *testing.T) {
				item := p.item
				// The date that WordPress did not decide is the one of the draft, which makes the entry a draft: it is the title
				// that this row wants.
				child := rulesPage(2, "child", 1, ch.status)
				imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", item, child))
				parentDoc, doc := docOf(docs, 1), docOf(docs, 2)
				if parentDoc.Entry.Status != p.want {
					t.Fatalf("the parent is %q, want %q", parentDoc.Entry.Status, p.want)
				}
				wantStatus, record := ch.want(parentDoc.Entry.Status)
				if doc.Entry.Status != wantStatus {
					t.Errorf("the child is %q, want %q", doc.Entry.Status, wantStatus)
				}
				if doc.Revision.Parent != parentDoc.Entry.ID {
					t.Errorf("the parent of the child is %q, want %q (it stays)", doc.Revision.Parent, parentDoc.Entry.ID)
				}
				var lines []string
				if record {
					lines = append(lines, notPublished(1))
				}
				if ch.status == "private" {
					lines = append(lines, private)
				}
				wantItem(t, imp, docs, 2, lines...)
			})
		}
	}

	t.Run("the pages are decided from the top down", func(t *testing.T) {
		items := []wxrEntry{
			rulesPage(1, "a", 0, "draft"), rulesPage(2, "b", 1, ""), rulesPage(3, "c", 2, ""), rulesPage(4, "d", 3, "future"),
			rulesPage(5, "e", 4, "future"), rulesPage(6, "f", 3, ""), rulesPage(7, "g", 0, ""), rulesPage(8, "h", 7, "future"),
			rulesPage(9, "i", 8, ""), rulesPage(10, "j", 8, "future"), rulesPage(11, "k", 7, ""),
		}
		// The WXR has the children first, the parents last, and in two orders.
		reversed := slices.Clone(items)
		slices.Reverse(reversed)
		for name, in := range map[string][]wxrEntry{"as written": items, "reversed": reversed} {
			t.Run(name, func(t *testing.T) {
				imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", in...))
				want := map[int64]content.Status{
					1: draft, 2: draft, 3: draft, 4: draft, 5: draft, 6: draft, 7: pub, 8: sched, 9: draft, 10: sched, 11: pub,
				}
				for post, w := range want {
					if got := docOf(docs, post).Entry.Status; got != w {
						t.Errorf("the status of item %d is %q, want %q", post, got, w)
					}
				}
				// Each page that is made a draft has one record, which names its own parent.
				for post, parent := range map[int64]int{2: 1, 3: 2, 4: 3, 5: 4, 6: 3, 9: 8} {
					wantItem(t, imp, docs, post, notPublished(parent))
				}
				for _, post := range []int64{1, 7, 8, 10, 11} {
					wantItem(t, imp, docs, post)
				}
			})
		}
	})
	t.Run("a page at the top is not looked at", func(t *testing.T) {
		imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", rulesPage(1, "a", 0, ""), rulesPage(2, "b", 99, "future")))
		if docOf(docs, 1).Entry.Status != pub || docOf(docs, 2).Entry.Status != sched {
			t.Errorf("the statuses are %q and %q", docOf(docs, 1).Entry.Status, docOf(docs, 2).Entry.Status)
		}
		wantItem(t, imp, docs, 1)
	})
	t.Run("a post is not looked at", func(t *testing.T) {
		_, _, docs := makeEntries(t, nil, nil, entriesWXR("", rulesPage(1, "a", 0, "draft"), wxrEntry{id: 2, title: "P", name: "p", parent: 1}))
		if got := docOf(docs, 2).Entry.Status; got != pub {
			t.Errorf("the status of the post is %q, want published", got)
		}
	})
	t.Run("a cycle that was not cut ends", func(t *testing.T) {
		_, c, items := rulesConv(t, entriesWXR("", rulesPage(1, "a", 0, ""), rulesPage(2, "b", 0, ""), rulesPage(3, "c", 0, "draft")))
		items[0].rev.Parent, items[1].rev.Parent, items[2].rev.Parent = items[1].id, items[2].id, items[0].id
		done := make(chan struct{})
		go func() {
			c.draftUnderUnpublished(items)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("draftUnderUnpublished did not end for pages that are the parents of each other")
		}
	})
}

func TestEntriesSlugs(t *testing.T) {
	same := func(slug, now string) string {
		return fmt.Sprintf("changed: the slug %q is also that of another entry of the type post; it is %q here -> {R}#/slug", slug, now)
	}
	samePage := func(slug, now string) string {
		return fmt.Sprintf("changed: the slug %q is also that of another entry of the type page; it is %q here -> {R}#/slug", slug, now)
	}
	long := strings.Repeat("a", content.MaxSlugBytes)
	cut := strings.Repeat("a", content.MaxSlugBytes-2)
	for _, tt := range []struct {
		name  string
		items []wxrEntry
		slugs map[int64]string
		lines map[int64][]string
	}{
		{
			name:  "slugs that differ",
			items: []wxrEntry{rulesPost(1, "a", ""), rulesPost(2, "b", "")},
			slugs: map[int64]string{1: "a", 2: "b"},
		},
		{
			name:  "two posts with a slug",
			items: []wxrEntry{rulesPost(1, "hello", ""), rulesPost(2, "hello", "")},
			slugs: map[int64]string{1: "hello", 2: "hello-2"},
			lines: map[int64][]string{2: {same("hello", "hello-2")}},
		},
		{
			name:  "three posts with a slug",
			items: []wxrEntry{rulesPost(1, "hello", ""), rulesPost(2, "hello", ""), rulesPost(3, "hello", "")},
			slugs: map[int64]string{1: "hello", 2: "hello-2", 3: "hello-3"},
			lines: map[int64][]string{2: {same("hello", "hello-2")}, 3: {same("hello", "hello-3")}},
		},
		{
			name:  "a slug that is the number of another that WordPress had",
			items: []wxrEntry{rulesPost(1, "a", ""), rulesPost(2, "a", ""), rulesPost(3, "a-2", "")},
			slugs: map[int64]string{1: "a", 2: "a-3", 3: "a-2"},
			lines: map[int64][]string{2: {same("a", "a-3")}},
		},
		{
			name:  "a slug that WordPress had, and the same in upper case first",
			items: []wxrEntry{rulesPost(1, "About", ""), rulesPost(2, "about", "")},
			slugs: map[int64]string{1: "about-2", 2: "about"},
			lines: map[int64][]string{1: {
				`changed: the slug "About" is "about" here: normalized to lower case and NFC -> {R}#/slug`,
				same("about", "about-2"),
			}},
		},
		{
			name:  "an entry without a slug and one with its number",
			items: []wxrEntry{rulesPost(5, "", ""), rulesPost(6, "entry-5", "")},
			slugs: map[int64]string{5: "entry-5-2", 6: "entry-5"},
			lines: map[int64][]string{
				5: {`changed: the entry has no slug; it is "entry-5" here -> {R}#/slug`, same("entry-5", "entry-5-2")},
			},
		},
		{
			name:  "pages under different parents",
			items: []wxrEntry{rulesPage(1, "x", 0, ""), rulesPage(2, "a", 1, ""), rulesPage(3, "y", 0, ""), rulesPage(4, "a", 3, ""), rulesPage(5, "a", 0, "")},
			slugs: map[int64]string{1: "x", 2: "a", 3: "y", 4: "a", 5: "a"},
		},
		{
			name:  "pages under the same parent",
			items: []wxrEntry{rulesPage(1, "x", 0, ""), rulesPage(2, "a", 1, ""), rulesPage(3, "a", 1, "")},
			slugs: map[int64]string{1: "x", 2: "a", 3: "a-2"},
			lines: map[int64][]string{3: {samePage("a", "a-2")}},
		},
		{
			name:  "pages at the top, one of which lost its parent",
			items: []wxrEntry{rulesPage(1, "a", 99, ""), rulesPage(2, "a", 0, "")},
			slugs: map[int64]string{1: "a", 2: "a-2"},
			lines: map[int64][]string{
				1: {"changed: the parent, item 99, is not in the WXR; the page is at the top -> {R}#/parent"},
				2: {samePage("a", "a-2")},
			},
		},
		{
			name:  "a page and a post",
			items: []wxrEntry{rulesPage(1, "a", 0, ""), rulesPost(2, "a", "")},
			slugs: map[int64]string{1: "a", 2: "a"},
		},
		{
			name:  "a post and an item of a custom type",
			items: []wxrEntry{{id: 1, typ: "works", title: "W", name: "a"}, rulesPost(2, "a", "")},
			slugs: map[int64]string{1: "a", 2: "a"},
		},
		{
			name:  "two items of a custom type",
			items: []wxrEntry{{id: 1, typ: "works", title: "W", name: "a"}, {id: 2, typ: "works", title: "V", name: "a"}},
			slugs: map[int64]string{1: "a", 2: "a-2"},
			lines: map[int64][]string{2: {"changed: the slug \"a\" is also that of another entry of the type works; it is \"a-2\" here -> {R}#/slug"}},
		},
		{
			name:  "a draft that comes first",
			items: []wxrEntry{rulesPost(1, "a", "draft"), rulesPost(2, "a", "")},
			slugs: map[int64]string{1: "a", 2: "a"},
		},
		{
			name:  "a draft that comes last",
			items: []wxrEntry{rulesPost(1, "a", ""), rulesPost(2, "a", "draft")},
			slugs: map[int64]string{1: "a", 2: "a"},
		},
		{
			name: "private and pending items",
			items: []wxrEntry{
				rulesPost(1, "a", "private"), rulesPost(2, "a", "pending"), rulesPost(3, "a", ""), rulesPost(4, "a", "draft"),
			},
			slugs: map[int64]string{1: "a", 2: "a", 3: "a", 4: "a"},
			lines: map[int64][]string{
				1: {"review: the entry was private; it is a draft here -> {E}#/status"},
				2: {`changed: the status "pending" is a draft here -> {E}#/status`},
			},
		},
		{
			name:  "a scheduled entry and a published one",
			items: []wxrEntry{rulesPost(1, "a", "future"), rulesPost(2, "a", "")},
			slugs: map[int64]string{1: "a", 2: "a-2"},
			lines: map[int64][]string{2: {same("a", "a-2")}},
		},
		{
			name:  "two scheduled entries",
			items: []wxrEntry{rulesPost(1, "a", "future"), rulesPost(2, "a", "future")},
			slugs: map[int64]string{1: "a", 2: "a-2"},
			lines: map[int64][]string{2: {same("a", "a-2")}},
		},
		{
			name: "an entry that was made a draft keeps its slug",
			items: []wxrEntry{
				rulesPost(1, "a", ""), {id: 2, title: "", name: "a"}, {id: 3, typ: "page", title: "Page", name: "a", password: entryPassword},
			},
			slugs: map[int64]string{1: "a", 2: "a", 3: "a"},
			lines: map[int64][]string{
				2: {"review: the entry cannot be published: /title: missing title; it is a draft here -> {E}#/status"},
				3: {"review: the entry was protected by a password; it is a draft here, and the password is not imported -> {E}#/status"},
			},
		},
		{
			name:  "a draft that has the number of a slug",
			items: []wxrEntry{rulesPost(1, "a", ""), rulesPost(2, "a", ""), rulesPost(3, "a-2", "draft")},
			slugs: map[int64]string{1: "a", 2: "a-2", 3: "a-2"},
			lines: map[int64][]string{2: {same("a", "a-2")}},
		},
		{
			name:  "drafts without a slug",
			items: []wxrEntry{rulesPost(1, "", "draft"), rulesPost(2, "", "draft"), rulesPost(3, "", "")},
			slugs: map[int64]string{1: "", 2: "", 3: "entry-3"},
			lines: map[int64][]string{3: {`changed: the entry has no slug; it is "entry-3" here -> {R}#/slug`}},
		},
		{
			// The first page is made a draft because its parent is only scheduled, so it does not take the slug of the second.
			name: "a page that is made a draft does not take a slug",
			items: []wxrEntry{
				rulesPage(1, "parent", 0, "future"), rulesPage(2, "a", 1, ""), rulesPage(3, "a", 1, "future"),
			},
			slugs: map[int64]string{1: "parent", 2: "a", 3: "a"},
			lines: map[int64][]string{2: {"review: the parent, page 1, is not published here; the page is a draft here -> {E}#/status"}},
		},
		{
			name:  "the longest slug",
			items: []wxrEntry{rulesPost(1, long, ""), rulesPost(2, long, ""), rulesPost(3, long, "")},
			slugs: map[int64]string{1: long, 2: cut + "-2", 3: cut + "-3"},
			lines: map[int64][]string{
				2: {same(long, cut+"-2")},
				3: {same(long, cut+"-3")},
			},
		},
		{
			// 200 bytes: "a", 66 characters of 3 bytes and "b". -2 does not fit after the 66th, so it is cut before it.
			name: "a slug that is cut in the middle of a character",
			items: []wxrEntry{
				rulesPost(1, "a"+strings.Repeat("%e3%81%82", 66)+"b", ""), rulesPost(2, "a"+strings.Repeat("%e3%81%82", 66)+"b", ""),
			},
			slugs: map[int64]string{1: "a" + strings.Repeat("あ", 66) + "b", 2: "a" + strings.Repeat("あ", 65) + "-2"},
			lines: map[int64][]string{
				2: {same("a"+strings.Repeat("あ", 66)+"b", "a"+strings.Repeat("あ", 65)+"-2")},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, _, docs := makeEntries(t, nil, nil, entriesWXR("", tt.items...))
			for post, want := range tt.slugs {
				if got := docOf(docs, post).Revision.Slug; got != want {
					t.Errorf("the slug of item %d is %q, want %q", post, got, want)
				}
				wantItem(t, imp, docs, post, tt.lines[post]...)
			}
		})
	}
}

func TestEntriesSlugsOfOtherLanguages(t *testing.T) {
	_, c, items := rulesConv(t, entriesWXR("", rulesPost(1, "hello", ""), rulesPost(2, "hello", ""), rulesPost(3, "hello", "")))
	items[1].entry.Language = "en"
	c.uniqueSlugs(items)
	for i, want := range []string{"hello", "hello", "hello-2"} {
		if got := items[i].rev.Slug; got != want {
			t.Errorf("the slug of item %d is %q, want %q", i+1, got, want)
		}
	}
}

func TestEntriesFrontPage(t *testing.T) {
	const (
		front      = "skipped: the front page, page %d, is not published here; it is left out of the site settings -> site/settings.json#/frontPage"
		noFront    = "skipped: the posts page, page %d, is left out: the site has no front page -> site/settings.json#/postsPage"
		postsOut   = "skipped: the posts page, page %d, is not published here; it is left out of the site settings -> site/settings.json#/postsPage"
		underDraft = "review: the parent, page 3, is not published here; the page is a draft here -> {E}#/status"
		private    = "review: the entry was private; it is a draft here -> {E}#/status"
	)
	for _, tt := range []struct {
		name                 string
		front, posts         int64
		items                []wxrEntry
		wantFront, wantPosts int64 // the items that are the front page and the posts page in the end, 0 for none
		lines                map[int64][]string
	}{
		{
			name: "both published", front: 1, posts: 2, wantFront: 1, wantPosts: 2,
			items: []wxrEntry{rulesPage(1, "home", 0, ""), rulesPage(2, "blog", 0, "")},
		},
		{
			name: "a front page only", front: 1, wantFront: 1,
			items: []wxrEntry{rulesPage(1, "home", 0, ""), rulesPage(2, "blog", 0, "")},
		},
		{
			name:  "neither",
			items: []wxrEntry{rulesPage(1, "home", 0, ""), rulesPage(2, "blog", 0, "draft")},
		},
		{
			name: "a front page that is a child", front: 2, posts: 3, wantFront: 2, wantPosts: 3,
			items: []wxrEntry{rulesPage(1, "a", 0, ""), rulesPage(2, "home", 1, ""), rulesPage(3, "blog", 1, "")},
		},
		{
			name: "a front page that is a draft", front: 1, posts: 2,
			items: []wxrEntry{rulesPage(1, "home", 0, "draft"), rulesPage(2, "blog", 0, "")},
			lines: map[int64][]string{1: {fmt.Sprintf(front, 1)}, 2: {fmt.Sprintf(noFront, 2)}},
		},
		{
			name: "a front page that is private", front: 1, posts: 2,
			items: []wxrEntry{rulesPage(1, "home", 0, "private"), rulesPage(2, "blog", 0, "")},
			lines: map[int64][]string{1: {private, fmt.Sprintf(front, 1)}, 2: {fmt.Sprintf(noFront, 2)}},
		},
		{
			name: "a front page that is scheduled", front: 1, posts: 2,
			items: []wxrEntry{rulesPage(1, "home", 0, "future"), rulesPage(2, "blog", 0, "")},
			lines: map[int64][]string{1: {fmt.Sprintf(front, 1)}, 2: {fmt.Sprintf(noFront, 2)}},
		},
		{
			name: "a front page that is a draft, and a posts page that is too", front: 1, posts: 2,
			items: []wxrEntry{rulesPage(1, "home", 0, "draft"), rulesPage(2, "blog", 0, "draft")},
			lines: map[int64][]string{1: {fmt.Sprintf(front, 1)}, 2: {fmt.Sprintf(noFront, 2)}},
		},
		{
			name: "only a front page that is a draft", front: 1,
			items: []wxrEntry{rulesPage(1, "home", 0, "draft")},
			lines: map[int64][]string{1: {fmt.Sprintf(front, 1)}},
		},
		{
			name: "a posts page that is a draft", front: 1, posts: 2, wantFront: 1,
			items: []wxrEntry{rulesPage(1, "home", 0, ""), rulesPage(2, "blog", 0, "draft")},
			lines: map[int64][]string{2: {fmt.Sprintf(postsOut, 2)}},
		},
		{
			name: "a posts page that is scheduled", front: 1, posts: 2, wantFront: 1,
			items: []wxrEntry{rulesPage(1, "home", 0, ""), rulesPage(2, "blog", 0, "future")},
			lines: map[int64][]string{2: {fmt.Sprintf(postsOut, 2)}},
		},
		{
			name: "a front page that comes under a draft", front: 1, posts: 2,
			items: []wxrEntry{rulesPage(1, "home", 3, ""), rulesPage(2, "blog", 0, ""), rulesPage(3, "parent", 0, "draft")},
			lines: map[int64][]string{1: {underDraft, fmt.Sprintf(front, 1)}, 2: {fmt.Sprintf(noFront, 2)}},
		},
		{
			name: "a posts page that comes under a draft", front: 1, posts: 2, wantFront: 1,
			items: []wxrEntry{rulesPage(1, "home", 0, ""), rulesPage(2, "blog", 3, ""), rulesPage(3, "parent", 0, "draft")},
			lines: map[int64][]string{2: {underDraft, fmt.Sprintf(postsOut, 2)}},
		},
		{
			name: "a front page that has no title", front: 1, posts: 2,
			items: []wxrEntry{{id: 1, typ: "page", name: "home"}, rulesPage(2, "blog", 0, "")},
			lines: map[int64][]string{
				1: {"review: the entry cannot be published: /title: missing title; it is a draft here -> {E}#/status", fmt.Sprintf(front, 1)},
				2: {fmt.Sprintf(noFront, 2)},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := rulesSettings(tt.front, tt.posts)
			imp, site, docs := makeEntries(t, s, nil, entriesWXR("", tt.items...))
			for _, w := range []struct {
				what string
				got  content.Localized
				post int64
			}{{"front page", site.FrontPage, tt.wantFront}, {"posts page", site.PostsPage, tt.wantPosts}} {
				switch {
				case w.post == 0 && w.got != nil:
					t.Errorf("the %s of the settings is %v, want none (nil)", w.what, w.got)
				case w.post != 0 && (len(w.got) != 1 || w.got["ja"] != postIDOf(t, imp, w.post)):
					t.Errorf("the %s of the settings is %v, want the item %d in ja", w.what, w.got, w.post)
				}
			}
			for i := range imp.WXR.Items {
				post := imp.WXR.Items[i].ID
				wantItem(t, imp, docs, post, tt.lines[post]...)
			}
		})
	}

	t.Run("a front page that is not made an entry", func(t *testing.T) {
		in := entriesWXR("",
			wxrEntry{id: 1, typ: "page", title: "Home", name: "home", date: zeroDate, dateGMT: zeroDate, modified: zeroDate, modifiedGMT: zeroDate},
			rulesPage(2, "blog", 0, ""),
		)
		s := rulesSettings(1, 2)
		imp, site := planEntries(t, s, nil, in)
		homeID := postIDOf(t, imp, 1)
		docs, err := collectEntries(context.Background(), imp, s, site)
		if err != nil {
			t.Fatal(err)
		}
		checkEntries(t, imp, site, docs)
		if site.FrontPage != nil || site.PostsPage != nil {
			t.Errorf("the settings have the front page %v and the posts page %v, want none", site.FrontPage, site.PostsPage)
		}
		var lines []string
		for _, l := range reportLines(t, imp.Report) {
			if strings.Contains(l, "site/settings.json#/") {
				lines = append(lines, l)
			}
		}
		ch := imp.WXR.Channel
		want := []string{
			fmt.Sprintf("site.xml:%d:%d: skipped: channel: the front page, entry %q, is not published here; it is left out of the site settings -> site/settings.json#/frontPage",
				ch.Line, ch.Column, homeID),
			itemWho(t, imp.WXR, "skipped", 2) + "the posts page, page 2, is left out: the site has no front page -> site/settings.json#/postsPage",
		}
		sameSet(t, "the records about the settings", lines, want)
	})
}

func withLink(x wxrEntry, link string) wxrEntry {
	x.link = link
	return x
}

func TestEntriesPaths(t *testing.T) {
	const home = "https://example.com"
	japanese := "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b"
	items := []wxrEntry{
		withLink(rulesPost(1, "hello-world", ""), home+"/2026/10/05/hello-world/"),
		withLink(rulesPage(2, "about", 0, ""), home+"/about/"),
		withLink(rulesPage(3, "team", 2, ""), home+"/about/team/"),
		withLink(rulesPage(4, "tokyo", 3, ""), home+"/about/team/tokyo/"),
		withLink(rulesPage(5, japanese, 0, ""), home+"/"+japanese+"/"),
		withLink(rulesPage(6, "english", 5, ""), home+"/"+japanese+"/english/"),
		withLink(rulesPage(7, "home", 0, ""), home+"/"),
		withLink(rulesPage(8, "later", 2, "future"), home+"/about/later/"),
		withLink(rulesPage(9, "idea", 2, "draft"), home+"/?page_id=9"),
		withLink(wxrEntry{id: 10, typ: "works", title: "W", name: "w"}, home+"/works/w/"),
	}
	s := rulesSettings(7, 0)
	imp, site, docs := makeEntries(t, s, nil, entriesWXR("", items...))
	for _, d := range docs {
		if d.Entry.Status == content.StatusDraft {
			continue
		}
		path, err := site.EntryPath(&d.Entry, &d.Revision, imp.PathRef)
		want, ok := linkPath(parseHome(home), imp.WXR.Post(d.Post).Link)
		if err != nil || !ok || rawPath(path) != want {
			t.Errorf("the path of item %d is %q, %v, want that of its link %q, %v", d.Post, path, err, want, ok)
		}
	}
	if d := docOf(docs, 7); d.Entry.Status != content.StatusPublished {
		t.Errorf("the front page is %q", d.Entry.Status)
	} else if path, err := site.EntryPath(&d.Entry, &d.Revision, imp.PathRef); err != nil || path != "/" {
		t.Errorf("the path of the front page is %q, %v, want /", path, err)
	}
	for post, want := range map[int64]content.PathRef{
		2: {Slug: "about"},
		3: {Slug: "team", Parent: postIDOf(t, imp, 2)},
		4: {Slug: "tokyo", Parent: postIDOf(t, imp, 3)},
		5: {Slug: "お知らせ"},
		6: {Slug: "english", Parent: postIDOf(t, imp, 5)},
		7: {Slug: "home"},
		8: {Slug: "later", Parent: postIDOf(t, imp, 2)},
	} {
		if got, ok := imp.PathRef(content.RefEntry, postIDOf(t, imp, post)); !ok || got != want {
			t.Errorf("PathRef of item %d = %+v, %v, want %+v", post, got, ok, want)
		}
	}
	for _, post := range []int64{1, 9, 10} {
		if got, ok := imp.PathRef(content.RefEntry, postIDOf(t, imp, post)); ok {
			t.Errorf("PathRef of item %d (a post, a draft, an item of a custom type) = %+v, want none", post, got)
		}
	}
	if _, ok := imp.PathRef(content.RefTerm, postIDOf(t, imp, 2)); ok {
		t.Error("PathRef gave a page as a term")
	}

	t.Run("the paths follow what the rules changed", func(t *testing.T) {
		in := entriesWXR("",
			rulesPage(1, "a", 0, ""), rulesPage(2, "a", 0, ""), rulesPage(3, "b", 99, ""), rulesPage(4, "c", 6, ""), rulesPage(5, "d", 4, ""),
			rulesPage(6, "e", 0, "draft"), rulesPage(7, "f", 2, ""),
		)
		imp, site, docs := makeEntries(t, nil, nil, in)
		for post, want := range map[int64]struct {
			path string
			ref  content.PathRef
			ok   bool
		}{
			1: {"/a/", content.PathRef{Slug: "a"}, true},
			2: {"/a-2/", content.PathRef{Slug: "a-2"}, true},
			3: {"/b/", content.PathRef{Slug: "b"}, true},
			7: {"/a-2/f/", content.PathRef{Slug: "f", Parent: postIDOf(t, imp, 2)}, true},
			// A draft and what is under it are drafts, and have no path.
			4: {}, 5: {}, 6: {},
		} {
			d := docOf(docs, post)
			ref, ok := imp.PathRef(content.RefEntry, d.Entry.ID)
			if ref != want.ref || ok != want.ok {
				t.Errorf("PathRef of item %d = %+v, %v, want %+v, %v", post, ref, ok, want.ref, want.ok)
			}
			if !want.ok {
				continue
			}
			if got, err := site.EntryPath(&d.Entry, &d.Revision, imp.PathRef); err != nil || got != want.path {
				t.Errorf("the path of item %d is %q, %v, want %q", post, got, err, want.path)
			}
		}
	})
}

func TestEntriesRulesSameOutput(t *testing.T) {
	in := entriesWXR("",
		rulesPage(1, "a", 2, ""), rulesPage(2, "b", 3, ""), rulesPage(3, "c", 1, ""), rulesPage(4, "tail", 3, "future"),
		rulesPage(5, "d", 6, ""), rulesPage(6, "d", 99, "draft"), rulesPage(7, "d", 0, ""), rulesPage(8, "home", 0, ""),
		rulesPost(9, "p", ""), rulesPost(10, "p", ""), rulesPost(11, "P", ""), wxrEntry{id: 12, title: "Post", name: "p", parent: 8},
	)
	s := rulesSettings(8, 5)
	var texts []string
	for range 3 {
		imp, _, docs := makeEntries(t, s, nil, in)
		texts = append(texts, entriesText(t, imp, docs))
	}
	if texts[0] != texts[1] || texts[1] != texts[2] {
		t.Errorf("conversions of the same WXR differ:\n%s\n%s", texts[0], texts[1])
	}
}

// TestEntriesRulesLarge checks that the rules across entries neither recurse nor take more than a time that is linear in
// the number of the entries: a chain of 10,000 pages, a ring of them, and 10,000 items with one slug.
func TestEntriesRulesLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("large inputs")
	}
	const n = 10000
	const limit = 10 * time.Second
	run := func(t *testing.T, items []wxrEntry) (*Import, []*EntryDoc) {
		t.Helper()
		imp, site := planEntries(t, nil, nil, entriesWXR("", items...))
		start := time.Now()
		docs, err := collectEntries(context.Background(), imp, entrySettings(), site)
		if err != nil {
			t.Fatalf("Entries = %v", err)
		}
		if took := time.Since(start); took > limit {
			t.Errorf("%d entries took %v, want less than %v", len(items), took, limit)
		}
		if len(docs) != len(items) {
			t.Fatalf("%d entries, want %d", len(docs), len(items))
		}
		checkAcrossEntries(t, imp, site, docs)
		return imp, docs
	}
	chain := func(topStatus string) []wxrEntry {
		items := make([]wxrEntry, n)
		for i := range items {
			items[i] = rulesPage(i+1, fmt.Sprintf("p%d", i+1), i, "")
		}
		items[0].status = topStatus
		return items
	}
	count := func(imp *Import, kind Kind, prefix string) int {
		total := 0
		for _, e := range imp.Report.Entries() {
			if e.Kind == kind && strings.HasPrefix(e.Message, prefix) {
				total++
			}
		}
		return total
	}
	postOf := func(docs []*EntryDoc) map[string]int64 {
		m := make(map[string]int64, len(docs))
		for _, d := range docs {
			m[d.Entry.ID] = d.Post
		}
		return m
	}

	t.Run("a chain of 10,000 pages under a draft", func(t *testing.T) {
		imp, docs := run(t, chain("draft"))
		posts := postOf(docs)
		for _, d := range docs {
			if d.Entry.Status != content.StatusDraft {
				t.Fatalf("the page %d is %q, want a draft", d.Post, d.Entry.Status)
			}
			if got := posts[d.Revision.Parent]; got != d.Post-1 {
				t.Fatalf("the parent of the page %d is %d, want %d (the parent stays)", d.Post, got, d.Post-1)
			}
		}
		if got := count(imp, Review, "the parent, page "); got != n-1 {
			t.Errorf("%d records that a parent is not published, want %d", got, n-1)
		}
	})
	t.Run("a ring of 10,000 pages", func(t *testing.T) {
		// The page without a parent in the end is the top of a chain of 10,000 published pages.
		items := chain("")
		items[0].parent = n
		imp, docs := run(t, items)
		without := 0
		for _, d := range docs {
			if d.Revision.Parent == "" {
				without++
			}
			if d.Entry.Status != content.StatusPublished {
				t.Fatalf("the page %d is %q, want published", d.Post, d.Entry.Status)
			}
			if _, ok := imp.PathRef(content.RefEntry, d.Entry.ID); !ok {
				t.Fatalf("PathRef of the page %d is not found", d.Post)
			}
		}
		if without != 1 {
			t.Errorf("%d pages without a parent, want 1", without)
		}
		if got := count(imp, Changed, "the parents make a cycle"); got != 1 {
			t.Errorf("%d records of a cycle, want 1", got)
		}
		if got := len(imp.Report.Entries()); got != 1 {
			t.Errorf("%d records, want 1", got)
		}
	})
	t.Run("10,000 posts with one slug", func(t *testing.T) {
		items := make([]wxrEntry, n)
		for i := range items {
			items[i] = rulesPost(i+1, "same", "")
		}
		imp, docs := run(t, items)
		for i, d := range docs {
			want := "same"
			if i > 0 {
				want = fmt.Sprintf("same-%d", i+1)
			}
			if d.Revision.Slug != want {
				t.Fatalf("the slug of the post %d is %q, want %q", d.Post, d.Revision.Slug, want)
			}
		}
		if got := count(imp, Changed, "the slug "); got != n-1 {
			t.Errorf("%d records of a slug, want %d", got, n-1)
		}
	})
	t.Run("posts with one slug, and posts that have the numbers of that slug", func(t *testing.T) {
		// The first 5,000 posts have one slug, and the others have the slugs with the numbers 2 to 5001, which WordPress had, so
		// each of the first has to pass all of them: that is done once, not for each.
		items := make([]wxrEntry, n)
		for i := 1; i <= n/2; i++ {
			items[i-1] = rulesPost(i, "same", "")
			items[n/2+i-1] = rulesPost(n/2+i, fmt.Sprintf("same-%d", i+1), "")
		}
		imp, docs := run(t, items)
		if got := count(imp, Changed, "the slug "); got != n/2-1 {
			t.Errorf("%d records of a slug, want %d", got, n/2-1)
		}
		for j := 1; j <= n/2; j++ {
			want := "same"
			if j > 1 {
				want = fmt.Sprintf("same-%d", n/2+j)
			}
			if got := docOf(docs, int64(j)).Revision.Slug; got != want {
				t.Fatalf("the slug of the post %d is %q, want %q", j, got, want)
			}
		}
	})
}
