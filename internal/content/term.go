package content

import (
	"encoding/json/v2"
	"fmt"
)

// Term is a term of a taxonomy, such as a category or a tag, in a language.
type Term struct {
	ID               string
	Taxonomy         string
	Language         string
	TranslationGroup string
	Name             string
	Slug             string
	Parent           string // ID of the parent term, only in a hierarchical taxonomy
	Description      string
}

func (x *Term) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Term) decode(d *decoder) error {
	var v Term
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "taxonomy":
			v.Taxonomy, err = d.string()
		case "language":
			v.Language, err = d.string()
		case "translationGroup":
			v.TranslationGroup, err = d.string()
		case "name":
			v.Name, err = d.string()
		case "slug":
			v.Slug, err = d.string()
		case "parent":
			v.Parent, err = d.string()
		case "description":
			v.Description, err = d.string()
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

func (x Term) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion    int    `json:"schemaVersion"`
		ID               string `json:"id,omitempty"`
		Taxonomy         string `json:"taxonomy,omitempty"`
		Language         string `json:"language,omitempty"`
		TranslationGroup string `json:"translationGroup,omitempty"`
		Name             string `json:"name,omitempty"`
		Slug             string `json:"slug,omitempty"`
		Parent           string `json:"parent,omitempty"`
		Description      string `json:"description,omitempty"`
	}{
		SchemaVersion:    SchemaVersion,
		ID:               x.ID,
		Taxonomy:         x.Taxonomy,
		Language:         x.Language,
		TranslationGroup: x.TranslationGroup,
		Name:             x.Name,
		Slug:             x.Slug,
		Parent:           x.Parent,
		Description:      x.Description,
	})
}

// Normalize cleans up the texts in place as Revision.Normalize does and maps the slug to lower case.
func (x *Term) Normalize() {
	x.Name = normalizeText(x.Name, false)
	x.Slug = normalizeSlug(x.Slug)
	x.Description = normalizeText(x.Description, true)
}

// Validate checks x in site. Every item of a term is required, apart from Parent and Description.
// lookup tells about the parent as ValidateOptions.Lookup does; nil skips checking that the parent exists.
func (x Term) Validate(site *Site, lookup func(kind RefKind, id string) (Ref, bool)) error {
	c := checker{doc: &docState{opts: ValidateOptions{Lookup: lookup}, language: x.Language}}
	x.check(&c, site)
	return c.result()
}

func (x Term) check(c *checker, site *Site) {
	c.required("id", x.ID, func(s string) error { return checkID(RefTerm, s) })
	c.required("taxonomy", x.Taxonomy, func(s string) error {
		if site.Taxonomy(s) == nil {
			return fmt.Errorf("unknown taxonomy %q", s)
		}
		return nil
	})
	c.required("language", x.Language, func(s string) error { return checkSiteLanguage(site, s) })
	c.required("translationGroup", x.TranslationGroup, func(s string) error { return checkID(RefTranslationGroup, s) })
	c.required("name", x.Name, checkLine)
	c.required("slug", x.Slug, checkSlug)
	if x.Parent != "" {
		x.checkParent(c, site, site.Taxonomy(x.Taxonomy))
	}
	if x.Description != "" {
		c.add("/description", checkText(x.Description, true))
	}
}

// checkParent checks the parent of x, whose taxonomy is tax (nil if the site has no such taxonomy).
func (x Term) checkParent(c *checker, site *Site, tax *TaxonomyDef) {
	switch {
	case tax != nil && !tax.Hierarchical:
		c.addf("/parent", "not allowed for %s, which is not hierarchical", tax.Name)
	case x.Parent == x.ID:
		c.addf("/parent", "a term cannot be its own parent")
	case tax == nil || checkSiteLanguage(site, x.Language) != nil:
		// Without a valid taxonomy and language to compare with, check only the ID and that the term exists.
		c.ref("/parent", RefTerm, x.Parent)
	default:
		c.term("/parent", x.Parent, tax.Name)
	}
}
