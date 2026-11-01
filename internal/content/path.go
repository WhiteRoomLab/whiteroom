package content

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// PathRef is what the lookup of EntryPath and TermPath tells about a page (RefEntry) or a term (RefTerm) on the
// public site, such as in the published revision of a page: its slug and the ID of its parent.
type PathRef struct {
	Slug   string
	Parent string
}

// EntryPath returns the path of e, whose content is r, such as "/2026/10/26/新サービス/" or "/en/about/": the path of
// the language of e if e is the front page in that language, or else the URL rule of the type of e after the path of
// the language, with the placeholders replaced. lookup finds the ancestors of a page for {path}, and the first
// category of e and its ancestors for {category}; nil finds none.
//
// The paths of a site follow its URL, as "/about/" does in "https://example.com/blog/about/", and start with "/".
// They are not percent-encoded, as in "/news/新サービス/". s must be valid.
func (s *Site) EntryPath(e *Entry, r *Revision, lookup func(kind RefKind, id string) (PathRef, bool)) (string, error) {
	if id := s.FrontPage[e.Language]; id != "" && id == e.ID {
		if _, err := s.entryType(e); err != nil {
			return "", err
		}
		return s.LanguagePath(e.Language), nil
	}
	return s.EntryRulePath(e, r, lookup)
}

// EntryRulePath returns the path of e, whose content is r, by the URL rule of its type, as EntryPath does, even if e is
// the front page in its language: "/home/" for the front page whose slug is "home", which EntryPath puts at "/".
func (s *Site) EntryRulePath(e *Entry, r *Revision, lookup func(kind RefKind, id string) (PathRef, bool)) (string, error) {
	t, err := s.entryType(e)
	if err != nil {
		return "", err
	}
	rule := t.URLRule()
	return s.fillRule(e.Language, rule, func(name string) (string, error) {
		switch name {
		case "slug":
			return pathSlug(r.Slug)
		case "path":
			return treePath(RefEntry, e.ID, r.Slug, r.Parent, lookup)
		case "number":
			if e.Number == 0 {
				return "", errors.New("missing number")
			}
			if err := checkNumber(e.Number); err != nil {
				return "", err
			}
			return strconv.Itoa(e.Number), nil
		case "yyyy", "mm", "dd":
			return formatDate(r.Date, dateLayouts[name])
		case "category":
			ids := r.Terms[TaxonomyCategory]
			if len(ids) == 0 {
				return "", errors.New("missing category")
			}
			slugs, err := lookupSlugs(RefTerm, ids[0], "", lookup)
			if err != nil {
				return "", err
			}
			return strings.Join(slugs, "/"), nil
		}
		return "", placeholderError(rule, name, t.Name)
	})
}

// entryType returns the type of e, or an error if s does not have the language or the type of e.
func (s *Site) entryType(e *Entry) (*TypeDef, error) {
	if err := checkSiteLanguage(s, e.Language); err != nil {
		return nil, err
	}
	t := s.Type(e.Type)
	if t == nil {
		return nil, fmt.Errorf("unknown type %q", e.Type)
	}
	return t, nil
}

// TermPath returns the path of the list of the entries with t, such as "/category/news/press-release/" or
// "/en/tag/events/", as EntryPath does. lookup finds the ancestors of t for {path}; nil finds none.
func (s *Site) TermPath(t *Term, lookup func(kind RefKind, id string) (PathRef, bool)) (string, error) {
	if err := checkSiteLanguage(s, t.Language); err != nil {
		return "", err
	}
	tax := s.Taxonomy(t.Taxonomy)
	if tax == nil {
		return "", fmt.Errorf("unknown taxonomy %q", t.Taxonomy)
	}
	rule := tax.URLRule()
	return s.fillRule(t.Language, rule, func(name string) (string, error) {
		switch name {
		case "slug":
			return pathSlug(t.Slug)
		case "path":
			return treePath(RefTerm, t.ID, t.Slug, t.Parent, lookup)
		}
		return "", placeholderError(rule, name, tax.Name)
	})
}

// AuthorPath returns the path of the page of a, an author, in language, such as "/author/yamada/" or
// "/en/author/yamada/", as EntryPath does.
func (s *Site) AuthorPath(a *AuthorProfile, language string) (string, error) {
	if err := checkSiteLanguage(s, language); err != nil {
		return "", err
	}
	rule := s.AuthorURLRule()
	return s.fillRule(language, rule, func(name string) (string, error) {
		if name == "slug" {
			return pathSlug(a.Slug)
		}
		return "", placeholderError(rule, name, authorRuleKind.name)
	})
}

// ArchivePath returns the path of the list of the entries of the type typeName in language, such as "/en/works/", as
// EntryPath does, or "" if the type has none, such as post, whose list is on the front page or the posts page.
func (s *Site) ArchivePath(typeName, language string) (string, error) {
	if err := checkSiteLanguage(s, language); err != nil {
		return "", err
	}
	t := s.Type(typeName)
	if t == nil {
		return "", fmt.Errorf("unknown type %q", typeName)
	}
	if t.Archive == "" {
		return "", nil
	}
	return strings.TrimSuffix(s.LanguagePath(language), "/") + t.Archive, nil
}

// PagedPath returns the path of page n of the list at path, such as "/category/news/page/2/" for page 2 of
// "/category/news/", or path itself for the first page.
func PagedPath(path string, n int) string {
	if n <= 1 {
		return path
	}
	return strings.TrimSuffix(path, "/") + "/page/" + strconv.Itoa(n) + "/"
}

// OutputFile returns the name of the file to write the page at path to, relative to the root of the output:
// "index.html" in the directory of a path ending with "/", as "about/index.html" for "/about/", or the file of a path
// ending with ".html". It rejects paths that could leave the root or write hidden files, and segments longer than
// MaxFileNameBytes.
func OutputFile(path string) (string, error) {
	if err := checkOutputPath(path); err != nil {
		return "", fmt.Errorf("invalid path %q: %w", path, err)
	}
	if strings.HasSuffix(path, "/") {
		return path[1:] + "index.html", nil
	}
	return path[1:], nil
}

func checkOutputPath(path string) error {
	switch {
	case !strings.HasPrefix(path, "/"):
		return errors.New(`must start with "/"`)
	case !strings.HasSuffix(path, "/") && !strings.HasSuffix(path, ".html"):
		return errors.New(`must end with "/" or ".html"`)
	}
	// Without the empty strings before the first "/" and after a trailing one.
	segs := strings.Split(path, "/")[1:]
	if strings.HasSuffix(path, "/") {
		segs = segs[:len(segs)-1]
	}
	for _, seg := range segs {
		switch {
		case seg == "":
			return errors.New("empty segment")
		case seg == "." || seg == "..":
			return errors.New(`dot segments ("." and "..") are not allowed`)
		case strings.HasPrefix(seg, "."):
			// Such as ".htaccess", which web servers may read as their settings.
			return errors.New(`segments cannot start with "."`)
		case len(seg) > MaxFileNameBytes:
			return fmt.Errorf("segment too long: %d bytes (max %d)", len(seg), MaxFileNameBytes)
		}
	}
	return nil
}

// fillRule returns rule, with each placeholder replaced by what value returns for its name, after the path of
// language without its trailing "/".
func (s *Site) fillRule(language, rule string, value func(name string) (string, error)) (string, error) {
	parts, err := parseURLRule(rule)
	if err != nil {
		return "", fmt.Errorf("invalid URL rule %q: %w", rule, err)
	}
	var b strings.Builder
	b.WriteString(strings.TrimSuffix(s.LanguagePath(language), "/"))
	for _, p := range parts {
		v := p.text
		if p.placeholder {
			if v, err = value(p.text); err != nil {
				return "", err
			}
		}
		b.WriteString(v)
	}
	return b.String(), nil
}

// placeholderError reports the placeholder name in rule, which the paths of what have no value for.
func placeholderError(rule, name, what string) error {
	return fmt.Errorf("invalid URL rule %q: placeholder %q is not allowed for %s", rule, "{"+name+"}", what)
}

// pathSlug returns slug, the slug of the entity whose path it is for, or why it cannot be in the path.
func pathSlug(slug string) (string, error) {
	if slug == "" {
		return "", errors.New("missing slug")
	}
	if err := checkSlug(slug); err != nil {
		return "", fmt.Errorf("invalid slug: %w", err)
	}
	return slug, nil
}

// treePath returns the {path} of the page or the term id: the slugs of its ancestors and slug, its own, joined with
// "/". parent is the ID of its parent ("" for none), from which lookup finds the ancestors.
func treePath(kind RefKind, id, slug, parent string, lookup func(kind RefKind, id string) (PathRef, bool)) (string, error) {
	if _, err := pathSlug(slug); err != nil {
		return "", err
	}
	slugs, err := lookupSlugs(kind, parent, id, lookup)
	if err != nil {
		return "", err
	}
	return strings.Join(append(slugs, slug), "/"), nil
}

// lookupSlugs returns the slugs of the page or the term id and its ancestors, from the root, which lookup finds.
// self is the ID of the entity whose path needs them ("" for none), which cannot be one of them.
func lookupSlugs(kind RefKind, id, self string, lookup func(kind RefKind, id string) (PathRef, bool)) ([]string, error) {
	what := "term"
	if kind == RefEntry {
		what = "page"
	}
	var slugs []string
	seen := []string{self}
	for id != "" {
		if slices.Contains(seen, id) {
			return nil, fmt.Errorf("the parents of %q make a cycle", id)
		}
		seen = append(seen, id)
		var ref PathRef
		ok := false
		if lookup != nil {
			ref, ok = lookup(kind, id)
		}
		switch {
		case !ok:
			return nil, fmt.Errorf("%s %q is not found", what, id)
		case ref.Slug == "":
			return nil, fmt.Errorf("%s %q has no slug", what, id)
		}
		if err := checkSlug(ref.Slug); err != nil {
			return nil, fmt.Errorf("%s %q has an invalid slug: %w", what, id, err)
		}
		slugs = append(slugs, ref.Slug)
		id = ref.Parent
	}
	slices.Reverse(slugs)
	return slugs, nil
}

// dateLayouts are the layouts of time.Format for the placeholders of the date.
var dateLayouts = map[string]string{"yyyy": "2006", "mm": "01", "dd": "02"}

// formatDate returns date, the date and time of a post, in layout.
func formatDate(date, layout string) (string, error) {
	if date == "" {
		return "", errors.New("missing date")
	}
	if err := checkDatetime(date); err != nil {
		return "", err
	}
	// In the offset written in date, not in the time zone of the site.
	d, _ := time.Parse(time.RFC3339, date)
	return d.Format(layout), nil
}
