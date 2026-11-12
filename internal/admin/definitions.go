package admin

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// schemaAnswer is the JSON of GET /api/schema: the types and the taxonomies that the site has, those that are built in with
// what the schema file sets for them, and then the others in the order of the schema file (6.4.4).
type schemaAnswer struct {
	AuthorURL  string           `json:"authorUrl"`
	Types      []typeAnswer     `json:"types"`
	Taxonomies []taxonomyAnswer `json:"taxonomies"`
}

type typeAnswer struct {
	Name       string        `json:"name"`
	Label      string        `json:"label,omitempty"`
	URL        string        `json:"url"`
	Archive    string        `json:"archive,omitempty"`
	Body       bool          `json:"body"`
	Taxonomies []string      `json:"taxonomies"`
	Fields     []fieldAnswer `json:"fields"`
}

type taxonomyAnswer struct {
	Name         string `json:"name"`
	Label        string `json:"label,omitempty"`
	URL          string `json:"url"`
	Hierarchical bool   `json:"hierarchical"`
}

// fieldAnswer is a field definition with the names of 6.4.4. What is zero, empty or nil is left out, but translatable, whose
// default is true: false is a value.
type fieldAnswer struct {
	Name         string            `json:"name,omitempty"` // the item of a list has none
	Type         content.FieldType `json:"type"`
	Label        string            `json:"label,omitempty"`
	Required     bool              `json:"required,omitzero"`
	Translatable bool              `json:"translatable"`
	MinLength    int               `json:"minLength,omitzero"`
	MaxLength    int               `json:"maxLength,omitzero"`
	Min          *float64          `json:"min,omitzero"`
	Max          *float64          `json:"max,omitzero"`
	Options      []string          `json:"options,omitempty"`
	Multiple     bool              `json:"multiple,omitzero"`
	MinItems     int               `json:"minItems,omitzero"`
	MaxItems     int               `json:"maxItems,omitzero"`
	Accept       []string          `json:"accept,omitempty"`
	Types        []string          `json:"types,omitempty"`
	Taxonomy     string            `json:"taxonomy,omitempty"`
	Fields       []fieldAnswer     `json:"fields,omitempty"`
	Of           *fieldAnswer      `json:"of,omitempty"`
}

// schemaOf returns the definitions of site, which has the types and the taxonomies of a schema file.
func schemaOf(site *content.Site) schemaAnswer {
	a := schemaAnswer{AuthorURL: site.AuthorURLRule(), Types: []typeAnswer{}, Taxonomies: []taxonomyAnswer{}}
	types := []string{content.TypePost, content.TypePage}
	for _, t := range site.Types {
		if !slices.Contains(types, t.Name) {
			types = append(types, t.Name)
		}
	}
	for _, name := range types {
		t := site.Type(name)
		a.Types = append(a.Types, typeAnswer{
			Name: t.Name, Label: t.Label, URL: t.URLRule(), Archive: t.Archive, Body: t.Body, Taxonomies: t.Taxonomies,
			Fields: fieldsOf(t.Fields),
		})
	}
	taxonomies := []string{content.TaxonomyCategory, content.TaxonomyTag}
	for _, t := range site.Taxonomies {
		if !slices.Contains(taxonomies, t.Name) {
			taxonomies = append(taxonomies, t.Name)
		}
	}
	for _, name := range taxonomies {
		t := site.Taxonomy(name)
		a.Taxonomies = append(a.Taxonomies, taxonomyAnswer{Name: t.Name, Label: t.Label, URL: t.URLRule(), Hierarchical: t.Hierarchical})
	}
	return a
}

func fieldsOf(defs []content.FieldDef) []fieldAnswer {
	out := make([]fieldAnswer, 0, len(defs))
	for i := range defs {
		out = append(out, fieldOf(&defs[i]))
	}
	return out
}

func fieldOf(f *content.FieldDef) fieldAnswer {
	a := fieldAnswer{
		Name: f.Name, Type: f.Type, Label: f.Label, Required: f.Required, Translatable: !f.Synced,
		MinLength: f.MinLength, MaxLength: f.MaxLength, Min: f.Min, Max: f.Max, Options: f.Options, Multiple: f.Multiple,
		MinItems: f.MinItems, MaxItems: f.MaxItems, Accept: f.Accept, Types: f.Types, Taxonomy: f.Taxonomy,
		Fields: fieldsOf(f.Fields),
	}
	if f.Of != nil {
		of := fieldOf(f.Of)
		a.Of = &of
	}
	return a
}

// showSchema is GET /api/schema: the definitions that the Editor validates the documents against, from the schema file that
// it was made with.
func (s *Server) showSchema(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.at("checked")
	file := s.editor.Schema()
	site := new(content.Site)
	if err := site.UnmarshalSchema(file.Data); err != nil {
		s.fail(w, r, fmt.Errorf("%s: %w", file.Name, err))
		return
	}
	writeJSON(w, http.StatusOK, schemaOf(site))
}

// themeAnswer is the JSON of GET /api/theme: the settings of the theme with the names of theme.yaml (5.4.2), the lists in
// the order that it is written in; what it leaves out is as the build takes it.
type themeAnswer struct {
	DefaultLanguage string             `json:"defaultLanguage"`
	Presets         presetsAnswer      `json:"presets"`
	Styles          []string           `json:"styles"`
	Custom          customAnswer       `json:"custom"`
	Blocks          []themeBlockAnswer `json:"blocks"`
	Marks           marksAnswer        `json:"marks"`
	Locations       []nameAnswer       `json:"locations"`
	PageTemplates   []nameAnswer       `json:"pageTemplates"`
	ImageSizes      []imageSizeAnswer  `json:"imageSizes"`
	MobileWidth     string             `json:"mobileWidth"`
}

type presetsAnswer struct {
	Color      []presetAnswer `json:"color"`
	FontSize   []presetAnswer `json:"fontSize"`
	Spacing    []presetAnswer `json:"spacing"`
	FontFamily []presetAnswer `json:"fontFamily"`
	Gradient   []presetAnswer `json:"gradient"`
	Shadow     []presetAnswer `json:"shadow"`
}

type presetAnswer struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// customAnswer says for each kind that styles can also write directly whether it may be written so.
type customAnswer struct {
	Color    bool `json:"color"`
	FontSize bool `json:"fontSize"`
	Spacing  bool `json:"spacing"`
}

type themeBlockAnswer struct {
	Type     content.BlockType `json:"type"`
	Styles   []string          `json:"styles"`
	Variants []nameAnswer      `json:"variants"`
}

type marksAnswer struct {
	Highlight highlightAnswer `json:"highlight"`
}

type highlightAnswer struct {
	Variants []nameAnswer `json:"variants"`
}

// nameAnswer is a name that the theme defines, with its label.
type nameAnswer struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

type imageSizeAnswer struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Width  int    `json:"width,omitzero"`
	Height int    `json:"height,omitzero"`
	Crop   bool   `json:"crop"`
}

func themeOf(t *content.ThemeSettings) themeAnswer {
	styles := t.Styles
	if styles == nil {
		styles = content.StyleItems()
	}
	a := themeAnswer{
		DefaultLanguage: t.DefaultLanguage,
		Presets: presetsAnswer{
			Color: presetsOf(t.Presets.Color), FontSize: presetsOf(t.Presets.FontSize), Spacing: presetsOf(t.Presets.Spacing),
			FontFamily: presetsOf(t.Presets.FontFamily), Gradient: presetsOf(t.Presets.Gradient), Shadow: presetsOf(t.Presets.Shadow),
		},
		Styles: styles,
		Custom: customAnswer{
			Color:    !slices.Contains(t.PresetsOnly, "color"),
			FontSize: !slices.Contains(t.PresetsOnly, "fontSize"),
			Spacing:  !slices.Contains(t.PresetsOnly, "spacing"),
		},
		Blocks:        make([]themeBlockAnswer, 0, len(t.Blocks)),
		Marks:         marksAnswer{Highlight: highlightAnswer{Variants: namesOf(t.HighlightVariants)}},
		Locations:     namesOf(t.Locations),
		PageTemplates: namesOf(t.PageTemplates),
		ImageSizes:    make([]imageSizeAnswer, 0, len(t.ImageSizes)),
		MobileWidth:   cmp.Or(t.MobileWidth, content.DefaultMobileWidth),
	}
	for _, b := range t.Blocks {
		blockStyles := b.Styles
		if blockStyles == nil {
			blockStyles = styles
		}
		a.Blocks = append(a.Blocks, themeBlockAnswer{Type: b.Type, Styles: blockStyles, Variants: namesOf(b.Variants)})
	}
	for _, size := range t.ImageSizes {
		a.ImageSizes = append(a.ImageSizes, imageSizeAnswer{
			Name: size.Name, Label: size.Label, Width: size.Width, Height: size.Height, Crop: size.Crop,
		})
	}
	return a
}

func presetsOf(ps []content.Preset) []presetAnswer {
	out := make([]presetAnswer, 0, len(ps))
	for _, p := range ps {
		out = append(out, presetAnswer{Name: p.Name, Value: p.Value})
	}
	return out
}

func namesOf(ns []content.ThemeName) []nameAnswer {
	out := make([]nameAnswer, 0, len(ns))
	for _, n := range ns {
		out = append(out, nameAnswer{Name: n.Name, Label: n.Label})
	}
	return out
}

// showTheme is GET /api/theme: the settings of the theme that the Editor validates the documents against.
func (s *Server) showTheme(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.at("checked")
	writeJSON(w, http.StatusOK, themeOf(s.editor.ThemeSettings()))
}
