package wpimport

import (
	"fmt"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// entriesByID returns the entries of items by the ID of the entry.
func entriesByID(items []*entryItem) map[string]*entryItem {
	byID := make(map[string]*entryItem, len(items))
	for _, ei := range items {
		byID[ei.id] = ei
	}
	return byID
}

// setParents makes the parent of each page of items of its wp:post_parent. A page is at the top, and it is recorded, if its
// parent is not in the WXR, is not imported, is not a page or is in another language, is the page itself, or if the page is
// the one of a cycle of parents that loses it. Items of other types have no parent: they are counted, for reportGroups.
func (c *entryConv) setParents(items []*entryItem) {
	byPost := make(map[int64]*entryItem, len(items))
	for _, ei := range items {
		byPost[ei.it.ID] = ei
	}
	var children []*entryItem
	for _, ei := range items {
		n := ei.it.Parent
		switch {
		case n == 0:
		case ei.entry.Type != content.TypePage:
			c.tally(groupKey{kind: groupParent, typ: ei.entry.Type}, ei.it)
		default:
			ei.rev.Parent = c.parentID(ei, byPost[n])
			if ei.rev.Parent != "" {
				children = append(children, ei)
			}
		}
	}
	c.breakParentCycles(children)
}

// parentID returns the ID of the entry parent, which is the entry of the item that wp:post_parent of the page ei names, or
// "" and a record that the page is at the top.
func (c *entryConv) parentID(ei, parent *entryItem) string {
	n := ei.it.Parent
	var why string
	switch {
	case c.imp.WXR.Post(n) == nil:
		why = fmt.Sprintf("the parent, item %d, is not in the WXR; the page is at the top", n)
	case parent == nil:
		why = fmt.Sprintf("the parent, item %d, is not imported; the page is at the top", n)
	case parent == ei:
		why = "the page is its own parent; it is at the top"
	case parent.entry.Type != content.TypePage:
		why = fmt.Sprintf("the parent, item %d, is not a page; the page is at the top", n)
	case parent.entry.Language != ei.entry.Language:
		why = fmt.Sprintf("the parent, page %d, is in another language; the page is at the top", n)
	default:
		return parent.id
	}
	c.revisionNote(Changed, ei, "/parent", "%s", why)
	return ""
}

type visit uint8

const (
	visitOnPath visit = iota + 1 // on the path that the search is following
	visitDone
)

// breakParentCycles takes the parent from one page of each cycle of parents: the one whose ID is the lowest. pages are
// those that have a parent.
func (c *entryConv) breakParentCycles(pages []*entryItem) {
	byID := entriesByID(pages)
	seen := make(map[*entryItem]visit, len(pages))
	for _, ei := range pages {
		var path []*entryItem
		cur := ei
		for cur != nil && seen[cur] == 0 {
			seen[cur] = visitOnPath
			path = append(path, cur)
			cur = byID[cur.rev.Parent]
		}
		if cur != nil && seen[cur] == visitOnPath {
			cycle := path[slices.Index(path, cur):]
			lowest := slices.MinFunc(cycle, func(a, b *entryItem) int { return strings.Compare(a.id, b.id) })
			lowest.rev.Parent = ""
			c.revisionNote(Changed, lowest, "/parent", "the parents make a cycle; the page is at the top")
		}
		for _, p := range path {
			seen[p] = visitDone
		}
	}
}

// draftUnderUnpublished makes a draft of each published page whose parent is not published, and of each scheduled page
// whose parent is neither published nor scheduled (6.7). It decides from the top down, so a page that it makes a draft does
// that to its children too. The parent of the page stays.
func (c *entryConv) draftUnderUnpublished(items []*entryItem) {
	byID := entriesByID(items)
	seen := make(map[*entryItem]visit, len(items))
	var chain []*entryItem
	for _, ei := range items {
		if ei.entry.Type != content.TypePage {
			continue
		}
		// The pages above ei that are not decided, from ei up. The cycles are cut, but a cycle would end this too.
		chain = chain[:0]
		for cur := ei; cur != nil && seen[cur] == 0; cur = byID[cur.rev.Parent] {
			seen[cur] = visitOnPath
			chain = append(chain, cur)
		}
		for _, page := range slices.Backward(chain) {
			seen[page] = visitDone
			c.checkParentStatus(page, byID[page.rev.Parent])
		}
	}
}

// checkParentStatus makes the page ei a draft if its status needs that of parent, which is nil for none, and parent does
// not have it.
func (c *entryConv) checkParentStatus(ei, parent *entryItem) {
	if parent == nil {
		return
	}
	switch ei.status {
	case content.StatusPublished:
		if parent.status == content.StatusPublished {
			return
		}
	case content.StatusScheduled:
		if publishing(parent.status) {
			return
		}
	case content.StatusDraft, content.StatusArchived:
		return
	}
	ei.status = content.StatusDraft
	c.entryNote(Review, ei, "/status", "the parent, page %d, is not published here; the page is a draft here", parent.it.ID)
}

// uniqueSlugs changes the slugs of the published and scheduled entries of one language and type (and parent, for pages)
// that are the same: the slugs that WordPress had come first, and the others get -2 and so on. A draft keeps its slug.
func (c *entryConv) uniqueSlugs(items []*entryItem) {
	var claimed []*entryItem
	var claims []slugClaim
	for _, ei := range items {
		if !publishing(ei.status) || ei.rev.Slug == "" {
			continue
		}
		scope := ei.entry.Language + "\x00" + ei.entry.Type
		if ei.entry.Type == content.TypePage {
			scope += "\x00" + ei.rev.Parent
		}
		claimed = append(claimed, ei)
		claims = append(claims, slugClaim{scope: scope, slug: ei.rev.Slug, first: ei.slugFirst})
	}
	for i, slug := range claimSlugs(claims) {
		ei := claimed[i]
		if slug == ei.rev.Slug {
			continue
		}
		c.revisionNote(Changed, ei, "/slug", "the slug %q is also that of another entry of the type %s; it is %q here",
			ei.rev.Slug, typeText(ei.entry.Type), slug)
		ei.rev.Slug = slug
	}
}

// leaveOutFrontPages takes the front page and the posts page out of the settings of the site, and records it, if the page
// is not a published entry here (6.7). A site without a front page has no posts page.
func (c *entryConv) leaveOutFrontPages(items []*entryItem) {
	site := c.site
	byID := entriesByID(items)
	published := func(id string) bool {
		ei := byID[id]
		return ei != nil && ei.status == content.StatusPublished
	}
	// leave records that the page id, which the settings have at pointer as what, is left out for why.
	leave := func(pointer, what, id, why string) {
		desc, src := fmt.Sprintf("entry %q", id), ChannelSource(c.imp.File, c.imp.WXR)
		if ei := byID[id]; ei != nil {
			desc, src = fmt.Sprintf("page %d", ei.it.ID), ItemSource(c.imp.File, ei.it)
		}
		c.imp.entryAt(Skipped, src, settingsTarget(pointer), "the %s, %s, %s", what, desc, why)
	}
	for _, l := range site.Languages {
		if id := site.FrontPage[l]; id != "" && !published(id) {
			leave("/frontPage", "front page", id, "is not published here; it is left out of the site settings")
			delete(site.FrontPage, l)
		}
		id := site.PostsPage[l]
		switch {
		case id == "":
		case site.FrontPage[l] == "":
			leave("/postsPage", "posts page", id, "is left out: the site has no front page")
			delete(site.PostsPage, l)
		case !published(id):
			leave("/postsPage", "posts page", id, "is not published here; it is left out of the site settings")
			delete(site.PostsPage, l)
		}
	}
	if len(site.FrontPage) == 0 {
		site.FrontPage = nil
	}
	if len(site.PostsPage) == 0 {
		site.PostsPage = nil
	}
}

// recordPaths remembers the slug and the parent of each published or scheduled page, which PathRef gives back.
func (c *entryConv) recordPaths(items []*entryItem) {
	for _, ei := range items {
		if ei.entry.Type == content.TypePage && publishing(ei.status) {
			c.imp.setPath(ei.id, content.PathRef{Slug: ei.rev.Slug, Parent: ei.rev.Parent})
		}
	}
}
