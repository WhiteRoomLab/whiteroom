package render

import (
	_ "embed"
	"strings"
	"text/template"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// classesCSS is the CSS of the classes that the core gives blocks, a template of text/template whose MobileWidth is
// the widest screen on which columns stack.
//
//go:embed classes.css
var classesCSS string

var classesTemplate = template.Must(template.New("classes.css").Option("missingkey=error").Parse(classesCSS))

// SiteCSS returns the CSS of a site for its theme, whose settings are s, to load before the theme's CSS: the presets as
// custom properties of :root, such as --font-size-large, which the CSS of pages refers to, and then the rules of the
// classes that the core gives blocks, whose columns stack on screens up to the mobileWidth of the theme. A nil s is no
// theme, which has no presets. Presets with invalid names or values, which a loaded theme cannot have, are left out.
func SiteCSS(s *content.ThemeSettings) string {
	var b strings.Builder
	width := content.DefaultMobileWidth
	if s != nil {
		if decls := presetDecls(&s.Presets); len(decls) > 0 {
			b.WriteString(rule(":root", decls) + "\n")
		}
		if content.CheckMobileWidth(s.MobileWidth) == nil {
			width = s.MobileWidth
		}
	}
	if err := classesTemplate.Execute(&b, map[string]string{"MobileWidth": width}); err != nil {
		panic(err)
	}
	return b.String()
}

// presetDecls returns the declarations of the custom properties of p, such as "--color-primary: #1e73be", in the order
// of the kinds of Presets and then in the written order, without the presets whose names or values are invalid.
func presetDecls(p *content.Presets) []string {
	kinds := []struct {
		kind    string
		presets []content.Preset
	}{
		{"color", p.Color}, {"fontSize", p.FontSize}, {"spacing", p.Spacing},
		{"fontFamily", p.FontFamily}, {"gradient", p.Gradient}, {"shadow", p.Shadow},
	}
	var decls []string
	for _, k := range kinds {
		for _, x := range k.presets {
			if content.CheckName(x.Name) == nil && content.CheckPresetValue(x.Value) == nil {
				decls = append(decls, presetVar(k.kind, x.Name)+": "+x.Value)
			}
		}
	}
	return decls
}
