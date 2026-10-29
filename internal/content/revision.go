package content

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
)

// Revision is the content of an entry, which each save adds as a new revision.
type Revision struct {
	Title         string
	Slug          string
	Excerpt       string
	FeaturedImage string // ID of an image
	Date          string // post: the date of publication in RFC 3339, as checkDatetime accepts
	Author        string // post: ID of the author
	Sticky        bool   // post
	Parent        string // page: ID of the parent page
	Order         int    // page
	Template      string // page: name of a template of the theme
	Terms         Terms
	SEO           SEO
	Fields        Fields
	Body          Blocks
}

type SEO struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	OGImage     string `json:"ogImage,omitempty"` // ID of an image
	NoIndex     bool   `json:"noindex,omitzero"`
	Canonical   string `json:"canonical,omitempty"`
}

// Terms lists the IDs of the terms of an entry by taxonomy.
type Terms map[string][]string

// Fields holds the values of the custom fields of an entry by name as JSON, which the definitions of its type give meaning to.
type Fields map[string]jsontext.Value

func (x *Revision) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }
func (x *SEO) UnmarshalJSON(data []byte) error      { return decodeJSON(x, data) }
func (x *Terms) UnmarshalJSON(data []byte) error    { return decodeJSON(x, data) }
func (x *Fields) UnmarshalJSON(data []byte) error   { return decodeJSON(x, data) }

func (x *Revision) decode(d *decoder) error {
	var v Revision
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "title":
			v.Title, err = d.string()
		case "slug":
			v.Slug, err = d.string()
		case "excerpt":
			v.Excerpt, err = d.string()
		case "featuredImage":
			v.FeaturedImage, err = d.string()
		case "date":
			v.Date, err = d.string()
		case "author":
			v.Author, err = d.string()
		case "sticky":
			v.Sticky, err = d.bool()
		case "parent":
			v.Parent, err = d.string()
		case "order":
			v.Order, err = d.int()
		case "template":
			v.Template, err = d.string()
		case "terms":
			err = v.Terms.decode(d)
		case "seo":
			err = v.SEO.decode(d)
		case "fields":
			err = v.Fields.decode(d)
		case "body":
			v.Body, err = (&blockReader{}).list(d, 1, true)
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

func (x *SEO) decode(d *decoder) error {
	var v SEO
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "title":
			v.Title, err = d.string()
		case "description":
			v.Description, err = d.string()
		case "ogImage":
			v.OGImage, err = d.string()
		case "noindex":
			v.NoIndex, err = d.bool()
		case "canonical":
			v.Canonical, err = d.string()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Terms) decode(d *decoder) error {
	v := make(Terms)
	err := d.object(func(taxonomy string) error {
		return d.array(func(int) error {
			id, err := d.string()
			v[taxonomy] = append(v[taxonomy], id)
			return err
		})
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

// decode reads the values in the form that MarshalJSON writes them, without spaces and with the shortest escapes in strings.
// The definitions of the fields are needed to read more.
func (x *Fields) decode(d *decoder) error {
	v := make(Fields)
	err := d.object(func(name string) error {
		raw, _, err := d.raw()
		if err != nil {
			return err
		}
		if err := raw.Format(); err != nil {
			return d.errorf("%v", err)
		}
		switch string(raw) {
		case "null":
			return d.errorf("null is not allowed")
		case `""`:
			return d.errorf("empty string is not allowed")
		case "{}":
			return d.errorf("empty object is not allowed")
		case "[]":
			return d.errorf("empty array is not allowed")
		}
		v[name] = raw
		return nil
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x Revision) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Title         string `json:"title,omitempty"`
		Slug          string `json:"slug,omitempty"`
		Excerpt       string `json:"excerpt,omitempty"`
		FeaturedImage string `json:"featuredImage,omitempty"`
		Date          string `json:"date,omitempty"`
		Author        string `json:"author,omitempty"`
		Sticky        bool   `json:"sticky,omitzero"`
		Parent        string `json:"parent,omitempty"`
		Order         int    `json:"order,omitzero"`
		Template      string `json:"template,omitempty"`
		Terms         Terms  `json:"terms,omitempty"`
		SEO           SEO    `json:"seo,omitzero"`
		Fields        Fields `json:"fields,omitempty"`
		Body          Blocks `json:"body,omitempty"`
	}{
		SchemaVersion: SchemaVersion,
		Title:         x.Title,
		Slug:          x.Slug,
		Excerpt:       x.Excerpt,
		FeaturedImage: x.FeaturedImage,
		Date:          x.Date,
		Author:        x.Author,
		Sticky:        x.Sticky,
		Parent:        x.Parent,
		Order:         x.Order,
		Template:      x.Template,
		Terms:         x.Terms,
		SEO:           x.SEO,
		Fields:        x.Fields,
		Body:          x.Body,
	})
}

// MarshalJSON writes the taxonomies in sorted order.
func (x Terms) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string][]string(x), json.Deterministic(true))
}

// MarshalJSON writes the names in sorted order.
func (x Fields) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]jsontext.Value(x), json.Deterministic(true))
}

// Normalize cleans up texts, HTML and URLs in place as Block.Normalize does, maps the slug to lower case
// and removes values of which nothing is left. t is the type of the entry, which gives the fields their meaning:
// the fields that t does not define, or all of them if t is nil, are left as they are.
func (x *Revision) Normalize(t *TypeDef) {
	x.Title = normalizeText(x.Title, false)
	x.Slug = normalizeSlug(x.Slug)
	x.Excerpt = normalizeText(x.Excerpt, true)
	x.SEO.Title = normalizeText(x.SEO.Title, false)
	x.SEO.Description = normalizeText(x.SEO.Description, false)
	if x.SEO.Canonical != "" {
		x.SEO.Canonical = normalizeHref(x.SEO.Canonical)
	}
	if t != nil {
		x.Fields.normalize(t.Fields)
	}
	x.Body.Normalize()
}

// normalize normalizes the values of the fields fs in place and removes those of which nothing is left.
// It leaves the values of unknown names as they are.
func (x *Fields) normalize(fs []FieldDef) {
	for name, v := range *x {
		f := field(fs, name)
		if f == nil {
			continue
		}
		if n := f.normalize(v); n != nil {
			(*x)[name] = n
		} else {
			delete(*x, name)
		}
	}
	// Empty is nil, as UnmarshalJSON makes it.
	if len(*x) == 0 {
		*x = nil
	}
}

type RevisionOptions struct {
	Site     *Site
	Entry    *Entry    // the state of the entry; the revision is checked with its ID, type and language
	Publish  bool      // the revision is to be published or scheduled, which needs the required items
	Import   bool      // as in ValidateOptions
	Previous *Revision // the previous revision of the entry, whose blocks are matched by ID as in ValidateOptions
	Lookup   func(kind RefKind, id string) (Ref, bool)
}

// Validate checks x as a revision of opts.Entry. Site and Entry must not be nil.
func (x Revision) Validate(opts RevisionOptions) error {
	t := opts.Site.Type(opts.Entry.Type)
	var prev Blocks
	if opts.Previous != nil {
		prev = opts.Previous.blocks(t)
	}
	c := checker{doc: &docState{
		opts:     ValidateOptions{AllowHTML: opts.Site.AllowHTML, Import: opts.Import, Previous: prev, Lookup: opts.Lookup},
		publish:  opts.Publish,
		language: opts.Entry.Language,
	}}
	x.check(&c, t, opts.Entry)
	return c.result()
}

// blocks returns the blocks of the body and the fields of x in one list.
func (x *Revision) blocks(t *TypeDef) Blocks {
	bs := slices.Clone(x.Body)
	if t == nil {
		return bs
	}
	for _, name := range slices.Sorted(maps.Keys(x.Fields)) {
		if f := field(t.Fields, name); f != nil {
			for _, b := range f.blocks(x.Fields[name]) {
				bs = append(bs, b...)
			}
		}
	}
	return bs
}

// check checks x as a revision of e, whose type is t (nil if the site has no such type).
func (x *Revision) check(c *checker, t *TypeDef, e *Entry) {
	if t == nil {
		c.addf("", "unknown type %q", e.Type)
	}
	publish := c.doc.publish
	if x.Title == "" {
		if publish {
			c.addf("/title", "missing title")
		}
	} else {
		c.add("/title", checkLine(x.Title))
	}
	if x.Slug == "" {
		if publish {
			c.addf("/slug", "missing slug")
		}
	} else {
		c.add("/slug", checkSlug(x.Slug))
	}
	if x.Excerpt != "" {
		c.add("/excerpt", checkText(x.Excerpt, true))
	}
	if x.FeaturedImage != "" {
		c.media("/featuredImage", x.FeaturedImage, MediaImage)
	}
	// of reports whether to check key, an item of the type name that is set if set is true; it reports key if t is another type.
	of := func(name, key string, set bool) bool {
		switch {
		case !set || t == nil:
			return false
		case t.Name != name:
			c.addf("/"+key, "not allowed for %s", t.Name)
			return false
		}
		return true
	}
	if t != nil && t.Name == TypePost && x.Date == "" && publish {
		c.addf("/date", "missing date")
	}
	if of(TypePost, "date", x.Date != "") {
		c.add("/date", checkDatetime(x.Date))
	}
	if of(TypePost, "author", x.Author != "") {
		c.ref("/author", RefAuthor, x.Author)
	}
	of(TypePost, "sticky", x.Sticky)
	if of(TypePage, "parent", x.Parent != "") {
		x.checkParent(c, e)
	}
	of(TypePage, "order", x.Order != 0)
	if of(TypePage, "template", x.Template != "") {
		c.add("/template", checkName(x.Template))
	}
	if t != nil {
		x.Terms.check(c, "/terms", t)
	}
	x.SEO.check(c, "/seo")
	if t != nil {
		checkFields(c, "/fields", t.Fields, x.Fields)
	}
	switch {
	case t == nil || t.Body:
		x.Body.check(c, "/body")
	case len(x.Body) > 0:
		c.addf("/body", "not allowed for %s, which has no body", t.Name)
	}
}

func (x *Revision) checkParent(c *checker, e *Entry) {
	if x.Parent == e.ID {
		c.addf("/parent", "an entry cannot be its own parent")
		return
	}
	r, ok := c.ref("/parent", RefEntry, x.Parent)
	if !ok {
		return
	}
	if r.Type != TypePage {
		c.addf("/parent", "entry %q is of the type %q (use page)", x.Parent, r.Type)
	}
	if r.Language != e.Language {
		c.addf("/parent", "entry %q is in the language %q, not %q", x.Parent, r.Language, e.Language)
	}
}

// check checks x, the terms of an entry of the type t, at path. Publishing needs a category if the URL rule of t
// has {category}. The taxonomies that t does not use are reported in sorted order after the others.
func (x Terms) check(c *checker, path string, t *TypeDef) {
	for _, taxonomy := range t.Taxonomies {
		p := path + "/" + escapePointer(taxonomy)
		ids, ok := x[taxonomy]
		if !ok {
			if rule := t.URLRule(); taxonomy == TaxonomyCategory && c.doc.publish && hasPlaceholder(rule, "category") {
				c.addf(p, "missing category; the URL rule %q of %s needs one", rule, t.Name)
			}
			continue
		}
		if len(ids) == 0 {
			c.addf(p, "empty value; omit it")
		}
		for i, id := range ids {
			ip := p + "/" + strconv.Itoa(i)
			if slices.Contains(ids[:i], id) {
				c.addf(ip, "duplicate term %q", id)
			} else {
				c.term(ip, id, taxonomy)
			}
		}
	}
	for _, taxonomy := range slices.Sorted(maps.Keys(x)) {
		if !slices.Contains(t.Taxonomies, taxonomy) {
			c.addf(path+"/"+escapePointer(taxonomy), "taxonomy %q is not used by %s", taxonomy, t.Name)
		}
	}
}

func (x SEO) check(c *checker, path string) {
	if x.Title != "" {
		c.add(path+"/title", checkLine(x.Title))
	}
	if x.Description != "" {
		c.add(path+"/description", checkLine(x.Description))
	}
	if x.OGImage != "" {
		c.media(path+"/ogImage", x.OGImage, MediaImage)
	}
	if x.Canonical != "" {
		c.add(path+"/canonical", checkCanonical(x.Canonical))
	}
}

// checkCanonical accepts what checkHref accepts, apart from mailto and tel URLs.
func checkCanonical(s string) error {
	if err := checkHref(s); err != nil {
		return err
	}
	if u, _ := url.Parse(s); u.Scheme == "mailto" || u.Scheme == "tel" {
		return fmt.Errorf("scheme %q is not allowed for canonical", u.Scheme)
	}
	return nil
}
