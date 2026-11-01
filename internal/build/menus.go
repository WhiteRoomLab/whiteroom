package build

import (
	"cmp"
	"html/template"
	"maps"
	"slices"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// menusOf returns the menus of language by location, whose items r renders (menuItems): every location of the theme,
// with nil for one without a menu in language (6.6.4). A menu whose location the theme does not have is left out, with
// a warning; one without a location is shown nowhere.
func (b *builder) menusOf(language string, r *render.Renderer) map[string][]MenuItem {
	menus := make(map[string][]MenuItem, len(b.theme.Settings.Locations))
	for _, l := range b.theme.Settings.Locations {
		menus[l.Name] = nil
	}
	for _, id := range slices.Sorted(maps.Keys(b.menus)) {
		d := b.menus[id]
		if d.v.Language != language || d.v.Location == "" {
			continue
		}
		if _, ok := menus[d.v.Location]; !ok {
			b.warnings.add(d.file, "/location", "the theme has no location %q; the menu is left out", d.v.Location)
			continue
		}
		menus[d.v.Location] = b.menuItems(d, d.v.Items, "/items", r)
	}
	return menus
}

// menuItems returns items, those of the menu d at path, whose links r renders: an item to an entry links to its page,
// with its title if the item has no label; one to a term, to its list, with its name; one with an href, as links do
// (6.5.2); one without a link is a heading of its children. An item to an entry that is not published is left out with
// its children, with a warning.
func (b *builder) menuItems(
	d *doc[content.Menu], items []content.MenuItem, path string, r *render.Renderer,
) []MenuItem {
	var out []MenuItem
	for i := range items {
		x := &items[i]
		p := path + "/" + strconv.Itoa(i)
		item := MenuItem{Label: x.Label}
		switch {
		case x.Entry != "":
			e := b.published(x.Entry)
			if e == nil {
				b.warnings.add(d.file, p, "entry %q is not published; the item and its children are left out", x.Entry)
				continue
			}
			item.Label, item.URL = cmp.Or(x.Label, e.rev.Title), r.URL(b.entryPaths[x.Entry])
		case x.Term != "":
			t := b.termLink(x.Term, r)
			item.Label, item.URL = cmp.Or(x.Label, t.Name), t.URL
		case x.Href != "":
			l, warnings := r.Link(&content.Link{Href: x.Href})
			for _, w := range warnings {
				b.warnings.add(d.file, p+"/href", "%s", w.Msg)
			}
			if l != nil {
				item.URL = l.Href
			}
		}
		if x.NewTab && item.URL != "" {
			item.Target, item.Rel = "_blank", "noopener"
		}
		item.Children = b.menuItems(d, x.Children, p+"/children", r)
		out = append(out, item)
	}
	return out
}

// currentMenus returns a copy of menus for the page at url, with the items that link to url as current.
func currentMenus(menus map[string][]MenuItem, url template.URL) map[string][]MenuItem {
	m := make(map[string][]MenuItem, len(menus))
	for location, items := range menus {
		m[location] = currentItems(items, url)
	}
	return m
}

// currentItems returns a copy of items, with their children, for the page at url, with those that link to url as
// current.
func currentItems(items []MenuItem, url template.URL) []MenuItem {
	if items == nil {
		return nil
	}
	out := make([]MenuItem, len(items))
	for i, x := range items {
		x.Current = x.URL == url
		x.Children = currentItems(x.Children, url)
		out[i] = x
	}
	return out
}
