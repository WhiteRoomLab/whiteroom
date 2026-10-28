package content

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Site is the part of the site settings that entries depend on.
type Site struct {
	Languages  []string      // language codes, such as "ja" and "en-US"
	AllowHTML  bool          // html blocks are enabled
	Types      []TypeDef     // custom types, and the taxonomies and fields that post and page add
	Taxonomies []TaxonomyDef // custom taxonomies
}

const (
	TypePost = "post"
	TypePage = "page"

	TaxonomyCategory = "category"
	TaxonomyTag      = "tag"
)

type TypeDef struct {
	Name       string
	Label      string
	Body       bool       // entries have a body; post and page always do
	Taxonomies []string   // for post, in addition to category and tag
	Fields     []FieldDef // custom fields
}

type TaxonomyDef struct {
	Name         string
	Label        string
	Hierarchical bool
}

var builtinTaxonomies = []TaxonomyDef{
	{Name: TaxonomyCategory, Hierarchical: true},
	{Name: TaxonomyTag},
}

// Type returns the definition of the type name, with what post and page always have, or nil if the site has no such type.
func (s *Site) Type(name string) *TypeDef {
	var def *TypeDef
	if i := slices.IndexFunc(s.Types, func(t TypeDef) bool { return t.Name == name }); i >= 0 {
		def = &s.Types[i]
	}
	if name != TypePost && name != TypePage {
		return def
	}
	t := &TypeDef{Name: name, Body: true}
	if name == TypePost {
		t.Taxonomies = []string{TaxonomyCategory, TaxonomyTag}
	}
	if def != nil {
		t.Label = def.Label
		t.Taxonomies = append(t.Taxonomies, def.Taxonomies...)
		t.Fields = def.Fields
	}
	return t
}

// Taxonomy returns the definition of the taxonomy name, built in or custom, or nil if the site has no such taxonomy.
func (s *Site) Taxonomy(name string) *TaxonomyDef {
	is := func(t TaxonomyDef) bool { return t.Name == name }
	if i := slices.IndexFunc(builtinTaxonomies, is); i >= 0 {
		t := builtinTaxonomies[i]
		return &t
	}
	if i := slices.IndexFunc(s.Taxonomies, is); i >= 0 {
		return &s.Taxonomies[i]
	}
	return nil
}

func (s *Site) hasTaxonomy(name string) bool {
	return s.Taxonomy(name) != nil
}

func (s *Site) Validate() error {
	var c checker
	s.check(&c)
	return c.result()
}

func (s *Site) check(c *checker) {
	if len(s.Languages) == 0 {
		c.addf("/languages", "missing languages")
	}
	for i, l := range s.Languages {
		p := "/languages/" + strconv.Itoa(i)
		if err := checkLanguage(l); err != nil {
			c.add(p, err)
		} else if slices.Contains(s.Languages[:i], l) {
			c.addf(p, "duplicate language %q", l)
		}
	}
	for i := range s.Taxonomies {
		t := &s.Taxonomies[i]
		p := "/taxonomies/" + escapePointer(t.Name)
		if err := checkName(t.Name); err != nil {
			c.add(p, err)
		} else if slices.ContainsFunc(builtinTaxonomies, func(b TaxonomyDef) bool { return b.Name == t.Name }) {
			c.addf(p, "%s is a built-in taxonomy", t.Name)
		} else if slices.ContainsFunc(s.Taxonomies[:i], func(o TaxonomyDef) bool { return o.Name == t.Name }) {
			c.addf(p, "duplicate taxonomy %q", t.Name)
		}
		if t.Label != "" {
			c.add(p+"/label", checkLine(t.Label))
		}
	}
	for i := range s.Types {
		t := &s.Types[i]
		p := "/types/" + escapePointer(t.Name)
		if err := checkName(t.Name); err != nil {
			c.add(p, err)
		} else if slices.ContainsFunc(s.Types[:i], func(o TypeDef) bool { return o.Name == t.Name }) {
			c.addf(p, "duplicate type %q", t.Name)
		}
		if t.Label != "" {
			c.add(p+"/label", checkLine(t.Label))
		}
		for j, name := range t.Taxonomies {
			tp := p + "/taxonomies/" + strconv.Itoa(j)
			switch {
			case !s.hasTaxonomy(name):
				c.addf(tp, "unknown taxonomy %q", name)
			case t.Name == TypePost && (name == TaxonomyCategory || name == TaxonomyTag):
				c.addf(tp, "post always uses %s", name)
			case slices.Contains(t.Taxonomies[:j], name):
				c.addf(tp, "duplicate taxonomy %q", name)
			}
		}
		checkFieldDefs(c, s, p+"/fields", t.Fields, 0)
	}
}

// languagePattern matches BCP 47 language tags with a language, an optional script and an optional region, in their canonical case.
var languagePattern = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Z][a-z]{3})?(?:-(?:[A-Z]{2}|[0-9]{3}))?$`)

func checkLanguage(s string) error {
	if !languagePattern.MatchString(s) {
		return fmt.Errorf(`invalid language code %q: use a language tag such as "ja", "en-US" or "zh-Hant"`, s)
	}
	return nil
}

func checkSiteLanguage(site *Site, language string) error {
	if !slices.Contains(site.Languages, language) {
		return fmt.Errorf("language %q is not in the site settings (use %s)", language, strings.Join(site.Languages, ", "))
	}
	return nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// escapePointer escapes s for a JSON Pointer.
func escapePointer(s string) string {
	return pointerEscaper.Replace(s)
}
