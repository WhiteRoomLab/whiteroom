package content

import (
	"encoding/json/v2"
	"fmt"
	"strconv"
)

// Menu is a tree of navigation links, such as those in the header or the footer, in a language.
type Menu struct {
	ID       string
	Title    string // the name in the admin screens
	Location string // a place that the theme defines, such as "header"; empty if shown nowhere
	Language string
	Items    []MenuItem
}

// MenuItem links to at most one of Entry, Term and Href; an item without a link is a heading of its children.
// Label can be omitted for an entry or a term, whose title or name is shown instead.
type MenuItem struct {
	Label    string     `json:"label,omitempty"`
	Entry    string     `json:"entry,omitempty"`
	Term     string     `json:"term,omitempty"`
	Href     string     `json:"href,omitempty"`
	NewTab   bool       `json:"newTab,omitzero"`
	Children []MenuItem `json:"children,omitempty"`
}

func (x *Menu) UnmarshalJSON(data []byte) error     { return decodeJSON(x, data) }
func (x *MenuItem) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Menu) decode(d *decoder) error {
	var v Menu
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "title":
			v.Title, err = d.string()
		case "location":
			v.Location, err = d.string()
		case "language":
			v.Language, err = d.string()
		case "items":
			v.Items, err = (&menuReader{}).list(d, 1)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	if !version {
		return d.errorf(`missing key "schemaVersion"`)
	}
	*x = v
	return nil
}

func (x *MenuItem) decode(d *decoder) error {
	r := menuReader{n: 1}
	v, err := r.item(d, 1)
	if err != nil {
		return err
	}
	*x = v
	return nil
}

// menuReader stops at MaxMenuDepth and MaxMenuItems; n counts the items read so far.
type menuReader struct{ n int }

// list reads a non-empty array of items at depth.
func (r *menuReader) list(d *decoder, depth int) ([]MenuItem, error) {
	// Element paths are built from base: before an element is read, d.pointer() is the previous one.
	base := d.pointer()
	var v []MenuItem
	err := d.array(func(i int) error {
		if depth > MaxMenuDepth {
			return &Error{Path: base, Msg: fmt.Sprintf("menu items are nested too deeply (max %d)", MaxMenuDepth)}
		}
		r.n++
		if r.n > MaxMenuItems {
			return &Error{Path: base + "/" + strconv.Itoa(i), Msg: fmt.Sprintf("too many menu items (max %d)", MaxMenuItems)}
		}
		item, err := r.item(d, depth)
		if err != nil {
			return err
		}
		v = append(v, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (r *menuReader) item(d *decoder, depth int) (MenuItem, error) {
	var v MenuItem
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "label":
			v.Label, err = d.string()
		case "entry":
			v.Entry, err = d.string()
		case "term":
			v.Term, err = d.string()
		case "href":
			v.Href, err = d.string()
		case "newTab":
			v.NewTab, err = d.bool()
		case "children":
			v.Children, err = r.list(d, depth+1)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return MenuItem{}, err
	}
	return v, nil
}

func (x Menu) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int        `json:"schemaVersion"`
		ID            string     `json:"id,omitempty"`
		Title         string     `json:"title,omitempty"`
		Location      string     `json:"location,omitempty"`
		Language      string     `json:"language,omitempty"`
		Items         []MenuItem `json:"items,omitempty"`
	}{
		SchemaVersion: SchemaVersion,
		ID:            x.ID,
		Title:         x.Title,
		Location:      x.Location,
		Language:      x.Language,
		Items:         x.Items,
	})
}

// Normalize cleans up the texts and URLs in place as Revision.Normalize does, also in the children.
func (x *Menu) Normalize() {
	x.Title = normalizeText(x.Title, false)
	for i := range x.Items {
		x.Items[i].normalize()
	}
}

func (x *MenuItem) normalize() {
	x.Label = normalizeText(x.Label, false)
	if x.Href != "" {
		x.Href = normalizeHref(x.Href)
	}
	for i := range x.Children {
		x.Children[i].normalize()
	}
}

// Validate checks x in site. ID, Title and Language are required.
// lookup tells about the entries and the terms of the items as ValidateOptions.Lookup does; nil skips checking that they exist.
func (x Menu) Validate(site *Site, lookup func(kind RefKind, id string) (Ref, bool)) error {
	c := checker{doc: &docState{opts: ValidateOptions{Lookup: lookup}}}
	x.check(&c, site)
	return c.result()
}

func (x Menu) check(c *checker, site *Site) {
	c.required("id", x.ID, func(s string) error { return checkID(RefMenu, s) })
	c.required("title", x.Title, checkLine)
	if x.Location != "" {
		c.add("/location", checkName(x.Location))
	}
	c.required("language", x.Language, func(s string) error { return checkSiteLanguage(site, s) })
	n := 0
	checkMenuItems(c, "/items", x.Items, 1, &n)
}

// checkMenuItems checks items, the list at path at depth; n counts the items checked so far.
// Entries and terms in any language are accepted, as in links.
func checkMenuItems(c *checker, path string, items []MenuItem, depth int, n *int) {
	if len(items) > 0 && depth > MaxMenuDepth {
		c.addf(path, "menu items are nested too deeply (max %d)", MaxMenuDepth)
		return
	}
	for i := range items {
		x := &items[i]
		p := path + "/" + strconv.Itoa(i)
		*n++
		if *n == MaxMenuItems+1 {
			c.addf(p, "too many menu items (max %d)", MaxMenuItems)
		}
		links := 0
		for _, s := range []string{x.Entry, x.Term, x.Href} {
			if s != "" {
				links++
			}
		}
		switch {
		case links > 1:
			c.addf(p, "set at most one of entry, term and href")
		case links == 0 && len(x.Children) == 0:
			c.addf(p, "menu item needs entry, term, href or children")
		}
		switch {
		case x.Label != "":
			c.add(p+"/label", checkLine(x.Label))
		case x.Entry == "" && x.Term == "":
			c.addf(p+"/label", "missing label")
		}
		if x.Entry != "" {
			c.ref(p+"/entry", RefEntry, x.Entry)
		}
		if x.Term != "" {
			c.ref(p+"/term", RefTerm, x.Term)
		}
		if x.Href != "" {
			c.add(p+"/href", checkHref(x.Href))
		}
		if x.NewTab && links == 0 {
			c.addf(p+"/newTab", "not allowed for an item without a link")
		}
		checkMenuItems(c, p+"/children", x.Children, depth+1, n)
	}
}
