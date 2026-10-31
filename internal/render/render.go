// Package render renders rich text and blocks into HTML, with the block templates of a theme or the default templates
// of the core.
package render

import (
	"fmt"
	"html/template"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// Resolver tells the renderer about the entities that content refers to by ID. The paths are from the site, as output
// paths are, such as "/about/" and "/en/about/"; the renderer puts the base before them.
type Resolver interface {
	// Entry returns the title and the path of the page of the entry with the ID. ok is false if there is no such
	// entry; path is "" if it is not published.
	Entry(id string) (title, path string, ok bool)
	// Media returns the medium with the ID and the path of its published file; ok is false if there is none.
	Media(id string) (m *content.Media, path string, ok bool)
	// Form returns the form with the ID; ok is false if there is none.
	Form(id string) (f *content.Form, ok bool)
}

// noResolver finds nothing.
type noResolver struct{}

func (noResolver) Entry(string) (title, path string, ok bool)            { return "", "", false }
func (noResolver) Media(string) (m *content.Media, path string, ok bool) { return nil, "", false }
func (noResolver) Form(string) (f *content.Form, ok bool)                { return nil, false }

// Renderer renders rich text and blocks into HTML for the pages in the language of a theme.Renderer. It is safe for
// concurrent use if its Resolver is.
type Renderer struct {
	tr         *theme.Renderer
	res        Resolver
	language   string
	base       string
	settings   *content.ThemeSettings                // of the theme; nil for a zero Theme, which has no presets
	highlights map[string]bool                       // the variants of the highlight mark that the theme has
	variants   map[content.BlockType]map[string]bool // the variants of each block type that the theme has
}

// New returns a Renderer for the pages of tr, which finds the entities that content refers to with res. A nil res
// finds nothing.
func New(tr *theme.Renderer, res Resolver) *Renderer {
	if res == nil {
		res = noResolver{}
	}
	r := &Renderer{
		tr:         tr,
		res:        res,
		language:   tr.Language(),
		base:       tr.Base(),
		settings:   tr.Settings(),
		highlights: make(map[string]bool),
		variants:   make(map[content.BlockType]map[string]bool),
	}
	if s := tr.Settings(); s != nil {
		for _, v := range s.HighlightVariants {
			r.highlights[v.Name] = true
		}
		for _, b := range s.Blocks {
			for _, v := range b.Variants {
				if r.variants[b.Type] == nil {
					r.variants[b.Type] = make(map[string]bool)
				}
				r.variants[b.Type][v.Name] = true
			}
		}
	}
	return r
}

// Warning is a problem that the renderer worked around, such as a link to an entry that is not published.
type Warning struct {
	List int    // the index of the list of blocks in the call of Document; 0 for RichText
	Path string // RFC 6901 JSON Pointer in that list or rich text, such as "/2/attrs/link"
	Msg  string
}

// String returns "<Path>: <Msg>", or Msg if Path is "".
func (w Warning) String() string {
	if w.Path == "" {
		return w.Msg
	}
	return w.Path + ": " + w.Msg
}

// Document renders the lists of blocks of one document, such as the body of a revision and its fields of type blocks,
// and returns the HTML of each, in order, and the CSS of their styles and highlights, which the page needs. The
// automatic anchors of headings avoid the anchors in all of the lists; of the blocks with CSS that have the same ID in
// them, only the first keeps its CSS. The error is that of executing a template, such as a block template of the theme
// that reads a field that the data lacks.
func (r *Renderer) Document(lists ...content.Blocks) ([]template.HTML, CSS, []Warning, error) {
	w := &walker{r: r, anchors: make(map[string]bool)}
	for _, bs := range lists {
		addAnchors(w.anchors, bs)
	}
	html := make([]template.HTML, len(lists))
	for i, bs := range lists {
		w.list = i
		s, err := w.blocks(bs, "")
		if err != nil {
			return nil, CSS{}, nil, err
		}
		html[i] = template.HTML(s)
	}
	return html, w.css, w.warnings, nil
}

// RichText renders rich text outside blocks, such as a richText field, and returns its HTML and the CSS of its
// highlights, which the page needs.
func (r *Renderer) RichText(rt content.RichText) (template.HTML, CSS, []Warning) {
	w := &walker{r: r}
	html := template.HTML(w.richText(rt, ""))
	return html, w.css, w.warnings
}

// walker holds the state of one call of the renderer, so that Renderer stays read only.
type walker struct {
	r        *Renderer
	list     int             // the index of the list of blocks that is being rendered
	anchors  map[string]bool // the anchors of the blocks of the document and the automatic anchors given so far
	classes  map[string]bool // the classes whose rules are in css, such as "b-0paragr1" and "hl-1a2b3c4d"
	css      CSS
	warnings []Warning
}

func (w *walker) warnf(path, format string, args ...any) {
	w.warnings = append(w.warnings, Warning{List: w.list, Path: path, Msg: fmt.Sprintf(format, args...)})
}

// warnAt inserts a warning at the index i of the warnings, before those that were given since.
func (w *walker) warnAt(i int, path, format string, args ...any) {
	w.warnings = slices.Insert(w.warnings, i, Warning{List: w.list, Path: path, Msg: fmt.Sprintf(format, args...)})
}
