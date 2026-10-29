package content

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	// checkTimeZone accepts the same names on systems without time zone data.
	_ "time/tzdata"
)

// Site is the site settings, such as the general settings, the languages, the URL rules, and the types and the taxonomies.
type Site struct {
	Name                  Localized
	Tagline               Localized
	URL                   string   // such as "https://example.com" or "https://example.com/blog"
	TimeZone              string   // an IANA name, such as "Asia/Tokyo"
	Logo                  string   // ID of an image
	Icon                  string   // ID of an image
	Languages             []string // language codes, such as "ja" and "en-US"
	DefaultLanguage       string
	PrefixDefaultLanguage bool      // the default language has a path prefix too, such as "/ja/"
	FrontPage             Localized // ID of the page to show at the front in each language, instead of the latest posts
	PostsPage             Localized // ID of the page to show the latest posts on, in each language with FrontPage
	PostsPerPage          int       // entries on each page of a list; 0 is DefaultPostsPerPage
	SEO                   SiteSEO
	AllowHTML             bool          // html blocks are enabled
	Types                 []TypeDef     // custom types, and the taxonomies and fields that post and page add
	Taxonomies            []TaxonomyDef // custom taxonomies
}

type SiteSEO struct {
	TitleFormat Localized // such as "{title} | {siteName}"; the languages without one use DefaultTitleFormat
	OGImage     Localized // ID of the default image for Open Graph in each language
	NoIndex     bool      // asks search engines not to index the whole site, such as before launch
}

const (
	DefaultPostsPerPage = 10
	DefaultTitleFormat  = "{title} | {siteName}"
)

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

// Validate checks s. Name in every language of s, URL, TimeZone, Languages and DefaultLanguage are required.
// lookup tells about the images and the pages that s refers to as ValidateOptions.Lookup does; nil skips checking
// that they exist and are images, or pages in the languages of their keys.
func (s *Site) Validate(lookup func(kind RefKind, id string) (Ref, bool)) error {
	c := checker{doc: &docState{opts: ValidateOptions{Lookup: lookup}}}
	s.check(&c)
	return c.result()
}

func (s *Site) check(c *checker) {
	s.Name.checkRequired(c, "/name", "name", s, false)
	s.Tagline.check(c, "/tagline", s, false)
	c.required("url", s.URL, checkSiteURL)
	c.required("timeZone", s.TimeZone, checkTimeZone)
	if s.Logo != "" {
		c.media("/logo", s.Logo, MediaImage)
	}
	if s.Icon != "" {
		c.media("/icon", s.Icon, MediaImage)
	}
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
	c.required("defaultLanguage", s.DefaultLanguage, func(l string) error { return checkSiteLanguage(s, l) })
	s.FrontPage.checkPages(c, "/frontPage", s)
	s.PostsPage.checkPages(c, "/postsPage", s)
	for _, l := range s.Languages {
		id := s.PostsPage[l]
		if id == "" {
			continue
		}
		p := "/postsPage/" + escapePointer(l)
		switch s.FrontPage[l] {
		case "":
			c.addf(p, "not allowed while the front page of %s shows the latest posts; set frontPage", l)
		case id:
			c.addf(p, "cannot be the front page of %s", l)
		}
	}
	if n := s.PostsPerPage; n != 0 && (n < 1 || n > MaxPostsPerPage) {
		c.addf("/postsPerPage", "postsPerPage %d is out of range (1 to %d)", n, MaxPostsPerPage)
	}
	s.SEO.TitleFormat.checkValues(c, "/seo/titleFormat", s, false, func(p, _, v string) { c.add(p, checkTitleFormat(v)) })
	s.SEO.OGImage.checkValues(c, "/seo/ogImage", s, false, func(p, _, id string) { c.media(p, id, MediaImage) })
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

// checkSiteURL accepts an http or https URL of the scheme and the host in lower case ASCII, an optional port other than
// the default, and an optional path not ending with "/", such as "https://example.com" or "https://example.com/blog".
func checkSiteURL(s string) error {
	if err := checkWebURL(s); err != nil {
		return err
	}
	u, _ := url.Parse(s)
	port := u.Port()
	n, err := strconv.Atoi(port)
	switch {
	case !strings.HasPrefix(s, u.Scheme):
		return errors.New("the scheme must be in lower case")
	case u.Hostname() == "":
		// checkHref passes "https://:8080", whose Host has only the port.
		return errors.New("missing host")
	case strings.ContainsFunc(u.Host, func(r rune) bool { return r >= utf8.RuneSelf }):
		return errors.New("the host must be ASCII; write a domain name in the xn-- form")
	case strings.ContainsFunc(u.Host, unicode.IsUpper):
		return errors.New("the host must be in lower case")
	case strings.HasSuffix(u.Host, ":"):
		return errors.New(`empty port; remove the ":"`)
	case port != "" && (err != nil || n < 1 || n > 65535):
		return fmt.Errorf("port %s is out of range (1 to 65535)", port)
	case u.Scheme == "http" && n == 80, u.Scheme == "https" && n == 443:
		return fmt.Errorf("port %s is the default for %s; omit it", port, u.Scheme)
	// Before checking the end: a query or a fragment may end with "/".
	case strings.ContainsAny(s, "?#"):
		return errors.New("queries and fragments are not allowed")
	case strings.HasSuffix(s, "/"):
		return errors.New(`must not end with "/"`)
	case slices.ContainsFunc(strings.Split(u.Path, "/")[1:], func(seg string) bool { return seg == "" || seg == "." || seg == ".." }):
		return errors.New(`the path cannot have empty, "." or ".." segments`)
	}
	return nil
}

// timeZonePattern matches the names in the time zone database, rejecting names such as "Asia/./Tokyo",
// which time.LoadLocation may find in the files of the system.
var timeZonePattern = regexp.MustCompile(`^[A-Za-z0-9+_-]+(?:/[A-Za-z0-9+_-]+)*$`)

// checkTimeZone accepts an IANA name of a time zone, but not "Local", which depends on the machine.
func checkTimeZone(s string) error {
	if s != "Local" && timeZonePattern.MatchString(s) {
		if _, err := time.LoadLocation(s); err == nil {
			return nil
		}
	}
	return fmt.Errorf(`unknown time zone %q: use an IANA name such as "Asia/Tokyo" or "UTC"`, s)
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
		if len(site.Languages) == 0 {
			return fmt.Errorf("language %q is not in the site settings", language)
		}
		return fmt.Errorf("language %q is not in the site settings (use %s)", language, strings.Join(site.Languages, ", "))
	}
	return nil
}

// checkPages checks x at path as checkEntries does, and that the entries are pages.
func (x Localized) checkPages(c *checker, path string, site *Site) {
	x.checkValues(c, path, site, false, func(p, language, id string) {
		r, ok := c.ref(p, RefEntry, id)
		if !ok {
			return
		}
		if r.Type != TypePage {
			c.addf(p, "entry %q is of the type %q (use page)", id, r.Type)
		}
		if r.Language != language {
			c.addf(p, "entry %q is in the language %q, not %q", id, r.Language, language)
		}
	})
}

// checkTitleFormat accepts a line with {title} once and {siteName} at most once, and no other braces.
func checkTitleFormat(s string) error {
	if err := checkLine(s); err != nil {
		return err
	}
	var titles, siteNames int
	rest := s
	for {
		i := strings.IndexAny(rest, "{}")
		if i < 0 {
			break
		}
		if rest[i] == '}' {
			return errors.New(`unexpected "}"`)
		}
		name, after, ok := strings.Cut(rest[i+1:], "}")
		if !ok {
			return errors.New(`unclosed "{"`)
		}
		switch name {
		case "title":
			titles++
		case "siteName":
			siteNames++
		default:
			return fmt.Errorf("unknown placeholder %q (use {title} and {siteName})", "{"+name+"}")
		}
		rest = after
	}
	switch {
	case titles == 0:
		return errors.New("missing {title}")
	case titles > 1:
		return errors.New("{title} can be used only once")
	case siteNames > 1:
		return errors.New("{siteName} can be used only once")
	}
	return nil
}

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// escapePointer escapes s for a JSON Pointer.
func escapePointer(s string) string {
	return pointerEscaper.Replace(s)
}
