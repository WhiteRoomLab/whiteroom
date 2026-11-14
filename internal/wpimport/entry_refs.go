package wpimport

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// metaNote adds an entry of the report about the postmeta m of the item ei, at pointer in the document of the revision.
func (c *entryConv) metaNote(kind Kind, ei *entryItem, m wxr.Meta, pointer, format string, args ...any) {
	target := Target{File: "revisions/" + ei.revID + ".json", Pointer: pointer}
	c.imp.entryAt(kind, MetaSource(ItemSource(c.imp.File, ei.it), m), target, format, args...)
}

// firstMeta returns the first of the postmeta l that has the key.
func firstMeta(l wxr.MetaList, key string) (wxr.Meta, bool) {
	for _, m := range l {
		if m.Key == key {
			return m, true
		}
	}
	return wxr.Meta{}, false
}

// lookup returns the ID of the entity of kind that the object o of WordPress became, if it is one that Ref knows.
func (c *entryConv) lookup(o Object, kind content.RefKind) (string, bool) {
	k, id, ok := c.imp.IDs.Lookup(o)
	if !ok || k != kind {
		return "", false
	}
	if _, ok := c.imp.Ref(kind, id); !ok {
		return "", false
	}
	return id, true
}

// groupKind is what a group of the items of one type has in common that is not imported.
type groupKind uint8

const (
	groupTerms groupKind = iota
	groupOrder
	groupTemplate
	groupParent
)

// groupKey is a group: the kind, the type of the entries and, for the terms, the taxonomy.
type groupKey struct {
	kind     groupKind
	typ      string
	taxonomy string
}

// itemGroup is the items of a group that count, and the first of them.
type itemGroup struct {
	first *wxr.Item
	n     int
}

// tally adds the item it to the group k, which reportGroups records once for all of its items.
func (c *entryConv) tally(k groupKey, it *wxr.Item) {
	g := c.groups[k]
	if g == nil {
		if c.groups == nil {
			c.groups = make(map[groupKey]*itemGroup)
		}
		g = &itemGroup{first: it}
		c.groups[k] = g
		c.groupKeys = append(c.groupKeys, k)
	}
	g.n++
}

// reportGroups records each group, once, at the place of its first item.
func (c *entryConv) reportGroups() {
	for _, k := range c.groupKeys {
		g := c.groups[k]
		src := Source{File: c.imp.File, Line: g.first.Line, Column: g.first.Column, Record: recordItem, Type: g.first.Type}
		items, typ := count(g.n, "item"), typeText(k.typ)
		var msg string
		switch k.kind {
		case groupTerms:
			msg = fmt.Sprintf("the terms of the taxonomy %s of %s of the post type %s are not imported; add %s to the taxonomies of %s in the schema file to import them",
				typeText(k.taxonomy), items, typ, typeText(k.taxonomy), typ)
		case groupOrder:
			msg = fmt.Sprintf("the menu order of %s of the post type %s is not imported; only pages have an order", items, typ)
		case groupTemplate:
			msg = fmt.Sprintf("the templates of %s of the post type %s are not imported; only pages have templates", items, typ)
		case groupParent:
			msg = fmt.Sprintf("the parents of %s of the post type %s are not imported; only pages have parents", items, typ)
		}
		c.imp.Report.Add(Entry{Kind: Skipped, Source: src, Message: msg})
	}
	c.groups, c.groupKeys = nil, nil
}

// termPick is a term of an item as the revision has it: its ID, and the term of the WXR that it was made of.
type termPick struct {
	id string
	t  *wxr.Term
}

// setTerms makes the terms of the revision of ei of the category elements of the item: those of the taxonomies that its
// type uses, which the import made, in the order of the item, and each once. If the URL rule of the type has {category},
// the first category is the one that WordPress used for the address of the item, and an item with none has the default
// category.
func (c *entryConv) setTerms(ei *entryItem) {
	it := ei.it
	typ := c.site.Type(ei.entry.Type)
	picks := make(map[string][]termPick)
	counted := make(map[string]bool)
	for _, ref := range it.Terms {
		name := taxonomyName(ref.Taxonomy, c.site)
		if name == "" {
			continue
		}
		if !slices.Contains(typ.Taxonomies, name) {
			if !counted[name] {
				counted[name] = true
				c.tally(groupKey{kind: groupTerms, typ: ei.entry.Type, taxonomy: name}, it)
			}
			continue
		}
		pointer := "/terms/" + pointerEscaper.Replace(name)
		t := c.imp.WXR.TermBySlug(ref.Taxonomy, ref.Slug)
		if t == nil {
			c.revisionNote(Skipped, ei, pointer, "the term %q of %s is not in the WXR; it is left out", ref.Slug, typeText(ref.Taxonomy))
			continue
		}
		id, ok := c.lookup(TermObject(t), content.RefTerm)
		if !ok {
			c.revisionNote(Skipped, ei, pointer, "the term %q of %s is not imported; it is left out", ref.Slug, typeText(ref.Taxonomy))
			continue
		}
		if !slices.ContainsFunc(picks[name], func(p termPick) bool { return p.id == id }) {
			picks[name] = append(picks[name], termPick{id: id, t: t})
		}
	}

	if slices.Contains(typ.Taxonomies, content.TaxonomyCategory) && strings.Contains(typ.URLRule(), "{category}") {
		if cats := picks[content.TaxonomyCategory]; len(cats) > 0 {
			picks[content.TaxonomyCategory] = c.firstCategory(ei, cats)
		} else if p, ok := c.defaultCategory(); ok {
			picks[content.TaxonomyCategory] = []termPick{p}
			c.revisionNote(Changed, ei, "/terms/"+content.TaxonomyCategory,
				"the entry has no category; it has the default category %q here, which {category} needs", c.opts.Settings.DefaultCategory)
		}
	}
	for name, list := range picks {
		if ei.rev.Terms == nil {
			ei.rev.Terms = make(content.Terms)
		}
		for _, p := range list {
			ei.rev.Terms[name] = append(ei.rev.Terms[name], p.id)
		}
	}
}

// firstCategory returns cats with first the category that WordPress used for the address of the item: the first by
// wp:term_id (those without one, as in WXR 1.0, after them) whose path by the URL rule of the type is the path of the
// link of the item, or if there is none, or the link or the path cannot be told, the one with the lowest wp:term_id. If that
// is the lowest, and the site had Yoast SEO, the primary category of the item is the first, as the link does not show it.
// The others keep their order.
func (c *entryConv) firstCategory(ei *entryItem, cats []termPick) []termPick {
	if len(cats) == 1 {
		return cats
	}
	byID := slices.Clone(cats)
	slices.SortStableFunc(byID, func(a, b termPick) int {
		switch {
		case a.t.ID >= 1 && b.t.ID >= 1:
			return cmp.Compare(a.t.ID, b.t.ID)
		case a.t.ID >= 1:
			return -1
		case b.t.ID >= 1:
			return 1
		}
		return 0
	})
	first := byID[0]
	if want, ok := linkPath(c.imp.urlIndex().home, ei.it.Link); ok {
		for _, cand := range byID {
			trial := ei.rev
			trial.Terms = content.Terms{content.TaxonomyCategory: {cand.id}}
			if got, err := c.site.EntryRulePath(&ei.entry, &trial, c.imp.PathRef); err == nil && rawPath(got) == want {
				first = cand
				break
			}
		}
	}
	if first.id == byID[0].id {
		if primary, ok := c.primaryCategory(ei, cats); ok {
			first = primary
		}
	}
	out := []termPick{first}
	for _, p := range cats {
		if p.id != first.id {
			out = append(out, p)
		}
	}
	return out
}

// defaultCategory returns the category of the default category of the settings, if the WXR has it and it is imported.
func (c *entryConv) defaultCategory() (termPick, bool) {
	if c.defaultCat.done {
		return c.defaultCat.pick, c.defaultCat.ok
	}
	c.defaultCat.done = true
	if c.opts.Settings == nil || c.opts.Settings.DefaultCategory == "" {
		return termPick{}, false
	}
	for i := range c.imp.WXR.Terms {
		t := &c.imp.WXR.Terms[i]
		if t.Taxonomy != "category" {
			continue
		}
		slug, err := wxr.DecodeSlug(t.Slug)
		if err != nil {
			slug = t.Slug
		}
		if slug != c.opts.Settings.DefaultCategory {
			continue
		}
		if id, ok := c.lookup(TermObject(t), content.RefTerm); ok {
			c.defaultCat.pick, c.defaultCat.ok = termPick{id: id, t: t}, true
		}
		break
	}
	return c.defaultCat.pick, c.defaultCat.ok
}

// setAuthor makes the author of the revision of ei, a post, of the author profile that the import made of its writer.
func (c *entryConv) setAuthor(ei *entryItem) {
	if ei.entry.Type != content.TypePost || ei.it.Creator == "" {
		return
	}
	if id, ok := c.lookup(UserObject(ei.it.Creator), content.RefAuthor); ok {
		ei.rev.Author = id
		return
	}
	c.revisionNote(Changed, ei, "/author", "the author of the post is not imported; the post has no author")
}

// setFeaturedImage makes the featured image of the revision of ei of the medium that the import made of the attachment
// that _thumbnail_id names.
func (c *entryConv) setFeaturedImage(ei *entryItem) {
	m, ok := firstMeta(ei.it.Meta, "_thumbnail_id")
	if !ok {
		return
	}
	value := strings.TrimSpace(m.Value)
	if value == "" {
		return
	}
	skip := func(format string, args ...any) { c.metaNote(Skipped, ei, m, "/featuredImage", format, args...) }
	n, err := strconv.ParseInt(value, 10, 64)
	if !isDigits(value) || err != nil || n < 1 {
		skip("the featured image %q is not the ID of an attachment", value)
		return
	}
	id, why := c.imageOf(n)
	if why != "" {
		skip("the featured image, attachment %d, %s", n, why)
		return
	}
	ei.rev.FeaturedImage = id
}

// imageOf returns the ID of the medium that the attachment n of WordPress became, or why it is not an image that is imported.
func (c *entryConv) imageOf(n int64) (id, why string) {
	id, ok := c.lookup(Object{Kind: ObjectPost, ID: n}, content.RefMedia)
	if !ok {
		return "", "is not imported"
	}
	if ref, _ := c.imp.Ref(content.RefMedia, id); ref.Type != content.MediaImage {
		return "", "is not an image"
	}
	return id, ""
}

// setSticky makes the revision of ei a sticky one if the post was.
func (c *entryConv) setSticky(ei *entryItem) {
	switch {
	case !ei.it.Sticky:
	case ei.entry.Type != content.TypePost:
		c.revisionNote(Skipped, ei, "/sticky", "the entry was sticky; only posts can be sticky")
	default:
		ei.rev.Sticky = true
	}
}

// setOrder makes the order of the revision of ei, a page, of wp:menu_order. The others are counted, for reportGroups.
func (c *entryConv) setOrder(ei *entryItem) {
	switch n := ei.it.MenuOrder; {
	case ei.entry.Type == content.TypePage:
		ei.rev.Order = int(n)
	case n != 0:
		c.tally(groupKey{kind: groupOrder, typ: ei.entry.Type}, ei.it)
	}
}

// setTemplate makes the template of the revision of ei, a page, of its page template of WordPress: the name that the
// settings give it, or else the one that its file name makes. The others are counted, for reportGroups.
func (c *entryConv) setTemplate(ei *entryItem) {
	file := pageTemplateOf(ei.it)
	if file == "" {
		return
	}
	if ei.entry.Type != content.TypePage {
		c.tally(groupKey{kind: groupTemplate, typ: ei.entry.Type}, ei.it)
		return
	}
	name, ok := "", false
	if s := c.opts.Settings; s != nil {
		name, ok = s.PageTemplates[file]
	}
	if !ok {
		name = templateName(file)
	}
	if name == "" {
		m, _ := firstMeta(ei.it.Meta, "_wp_page_template")
		c.metaNote(Skipped, ei, m, "/template",
			"the page template %q is not imported; pageTemplates of the settings maps it to a page template of the theme of the site", file)
		return
	}
	ei.rev.Template = name
}
