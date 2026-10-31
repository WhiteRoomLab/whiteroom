package theme

import (
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Kind is a kind of pages, which has its own order of page templates.
type Kind int

const (
	// KindEntry is the page of an entry: a post, a page or an entry of a custom type, also when it is the front page.
	KindEntry Kind = iota
	// KindPosts is the list of the latest posts: the front page of a language without frontPage, or the posts page.
	KindPosts
	// KindTerm is the list of the entries of a term.
	KindTerm
	// KindArchive is the list of the entries of a custom type.
	KindArchive
	// KindAuthor is the page of an author.
	KindAuthor
	// KindNotFound is the page for the paths that have no page.
	KindNotFound
)

// Page is what Template needs to know of a page.
type Page struct {
	Kind     Kind
	Type     string // of the entry for KindEntry, or of the entries for KindArchive, such as "post" or "works"
	Taxonomy string // of the term for KindTerm, such as "category"
	Template string // of a page (KindEntry with Type "page"), such as "landing", or "" for none
}

// Template returns the name of the page template for p: the first in the order of its kind that the theme has.
//
//   - KindEntry: entry-<type>.html, entry.html; for a page with a template, page-<template>.html first
//   - KindPosts: posts.html, list.html
//   - KindTerm: term-<taxonomy>.html, term.html, list.html
//   - KindArchive: archive-<type>.html, list.html
//   - KindAuthor: author.html, list.html
//   - KindNotFound: 404.html
//
// It returns "" only for KindNotFound when the theme has no 404.html: then the site has no such page. A page whose
// template the theme does not have, as after a change of themes, gets that of a page without one; the caller can
// compare the result with "page-" + p.Template + ".html" to warn. It panics if p.Kind is not one of the kinds.
func (t *Theme) Template(p Page) string {
	var names []string
	switch p.Kind {
	case KindEntry:
		if p.Type == content.TypePage && p.Template != "" {
			names = append(names, "page-"+p.Template+".html")
		}
		names = append(names, "entry-"+p.Type+".html", "entry.html")
	case KindPosts:
		names = []string{"posts.html", "list.html"}
	case KindTerm:
		names = []string{"term-" + p.Taxonomy + ".html", "term.html", "list.html"}
	case KindArchive:
		names = []string{"archive-" + p.Type + ".html", "list.html"}
	case KindAuthor:
		names = []string{"author.html", "list.html"}
	case KindNotFound:
		names = []string{"404.html"}
	default:
		panic(fmt.Sprintf("theme: unknown kind %d", p.Kind))
	}
	for _, name := range names {
		if t.namespaces[name] != nil {
			return name
		}
	}
	return ""
}

// Renderer executes the page templates and the block templates of a theme for the pages in one language, with the
// functions t and asset bound to that language and to the base of the URLs of the site. It is safe for concurrent use.
type Renderer struct {
	language  string
	texts     []map[string]string // the translations in which t looks up texts, in order
	base      string
	settings  *content.ThemeSettings
	assets    map[string]asset
	templates map[string]*template.Template            // clones of the namespaces of the page templates, by name
	blocks    map[content.BlockType]*template.Template // and of those of the block templates, by type
}

// Renderer returns a Renderer for the pages in language, a language code such as "ja" or "en-US". The function t
// looks up texts in the translation of language, then in those of the languages without its last subtags (for
// zh-Hant-TW, zh-Hant and zh), then in the default translation. The URLs of the assets start with base: the path or
// the URL of the site without a trailing "/", such as "/blog" or "https://example.com/blog", or "" for a site at the
// root of its host.
func (t *Theme) Renderer(language, base string) (*Renderer, error) {
	if err := content.CheckLanguage(language); err != nil {
		return nil, err
	}
	if strings.HasSuffix(base, "/") {
		return nil, fmt.Errorf(`invalid base %q: must not end with "/"; use "" for a site at the root`, base)
	}
	r := &Renderer{
		language: language,
		texts:    t.lookup(language),
		base:     base,
		settings: t.Settings,
		assets:   t.assets,
	}
	funcs := template.FuncMap{"t": r.t, "asset": r.asset, "dict": dict}
	r.templates = cloneNamespaces(t.namespaces, funcs)
	r.blocks = cloneNamespaces(t.blockNamespaces, funcs)
	return r, nil
}

// cloneNamespaces returns clones of namespaces with funcs bound.
func cloneNamespaces[K comparable](namespaces map[K]*template.Template, funcs template.FuncMap) map[K]*template.Template {
	clones := make(map[K]*template.Template, len(namespaces))
	for k, ns := range namespaces {
		c, err := ns.Clone()
		if err != nil {
			// Only an executed template cannot be cloned, and the namespaces are never executed.
			panic(err)
		}
		clones[k] = c.Funcs(funcs)
	}
	return clones
}

// Execute executes the page template name, as Template returns it, with data, and writes the page to w. A name that is
// not that of a page template of the theme is an error. The errors of the execution, such as for a key that a map in
// data does not have, are those of html/template.
func (r *Renderer) Execute(w io.Writer, name string, data any) error {
	tmpl := r.templates[name]
	if tmpl == nil {
		return fmt.Errorf("unknown page template %q", name)
	}
	return tmpl.Execute(w, data)
}

// HasBlock reports whether the theme has a block template for blocks of type t: blocks/<t>.html.
func (r *Renderer) HasBlock(t content.BlockType) bool {
	return r.blocks[t] != nil
}

// ExecuteBlock executes the block template of t with data, and writes the HTML to w. A type without a block template
// in the theme is an error.
func (r *Renderer) ExecuteBlock(w io.Writer, t content.BlockType, data any) error {
	tmpl := r.blocks[t]
	if tmpl == nil {
		return fmt.Errorf("no block template for %s blocks", t)
	}
	return tmpl.Execute(w, data)
}

// Language returns the language that r was made for.
func (r *Renderer) Language() string {
	return r.language
}

// Base returns the base of the URLs that r was made for.
func (r *Renderer) Base() string {
	return r.base
}

// Settings returns the settings of the theme; nil for a zero Theme.
func (r *Renderer) Settings() *content.ThemeSettings {
	return r.settings
}
