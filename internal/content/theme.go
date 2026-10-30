package content

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// ThemeSettings is the settings file of a theme, theme.yaml, which developers write.
type ThemeSettings struct {
	DefaultLanguage   string // language of the default translation
	Presets           Presets
	Styles            []string     // style items that blocks can use; nil: every style item
	PresetsOnly       []string     // kinds whose custom is false, in the order color, fontSize, spacing
	Blocks            []ThemeBlock // in the written order
	HighlightVariants []ThemeName  // variants of the highlight mark
	Locations         []ThemeName  // menu locations
	PageTemplates     []ThemeName  // templates of pages, in templates/page-<name>.html
	ImageSizes        []ImageSize
}

// Presets are the values that styles refer to by name, of each kind in the written order.
type Presets struct{ Color, FontSize, Spacing, FontFamily, Gradient, Shadow []Preset }

// Preset is a named CSS value, such as "#1e73be" or "clamp(1.25rem, 1rem + 1vw, 1.5rem)".
type Preset struct{ Name, Value string }

// ThemeBlock is what a theme sets for the blocks of Type.
type ThemeBlock struct {
	Type     BlockType
	Styles   []string // nil: the Styles of the theme
	Variants []ThemeName
}

// ThemeName is a name that a theme defines, such as a variant, with the label that the admin screen shows.
type ThemeName struct{ Name, Label string }

// ImageSize is a size that image blocks can choose by Name.
type ImageSize struct {
	Name, Label   string
	Width, Height int  // maximum in pixels; 0: no limit (at least one is set)
	Crop          bool // crop to exactly Width x Height
}

// styleItems are the items of Style, in its order.
var styleItems = []string{
	"textColor", "backgroundColor", "linkColor", "gradient", "fontSize", "letterSpacing", "fontFamily", "fontWeight",
	"lineHeight", "padding", "margin", "blockGap", "border",
}

// presetKinds are the keys of the kinds of presets, in the order of Presets.
var presetKinds = []string{"color", "fontSize", "spacing", "fontFamily", "gradient", "shadow"}

// customKinds are the kinds of presets that styles can also write directly, in the order of PresetsOnly.
var customKinds = []string{"color", "fontSize", "spacing"}

// kind returns the presets of the kind key, or nil for an unknown key.
func (p *Presets) kind(key string) *[]Preset {
	switch key {
	case "color":
		return &p.Color
	case "fontSize":
		return &p.FontSize
	case "spacing":
		return &p.Spacing
	case "fontFamily":
		return &p.FontFamily
	case "gradient":
		return &p.Gradient
	case "shadow":
		return &p.Shadow
	}
	return nil
}

// Unmarshal reads data, a settings file of a theme in the subset of YAML that internal/yaml reads, into s. The mappings
// keyed by names, such as those of presets, become lists in the written order. The items that data omits become zero,
// and s changes only if all of data is read without error. An error is an *Error at the line and the column of the
// problem. The values stay as written, without normalization: Validate reports the other problems, such as invalid
// names and unknown block types.
func (s *ThemeSettings) Unmarshal(data []byte) error {
	_, err := s.unmarshal(data)
	return err
}

// unmarshal is Unmarshal, and also returns the positions of the values in data by JSON Pointer, as yaml.ToJSON does, to
// locate the errors of Validate.
func (s *ThemeSettings) unmarshal(data []byte) (map[string]yaml.Position, error) {
	var v ThemeSettings
	positions, err := readYAML(data, v.decode)
	if err != nil {
		return nil, err
	}
	*s = v
	return positions, nil
}

// readYAML reads data, a file in the subset of YAML that internal/yaml reads, with read, and returns the positions of
// its values by JSON Pointer, as yaml.ToJSON does. An error is an *Error at the line and the column of the problem.
func readYAML(data []byte, read func(*decoder) error) (map[string]yaml.Position, error) {
	out, positions, err := yaml.ToJSON(data)
	if err != nil {
		var e *yaml.Error
		if !errors.As(err, &e) {
			return nil, &Error{Msg: err.Error()}
		}
		return nil, &Error{Msg: e.Msg, Line: e.Line, Column: e.Column}
	}
	if err := unmarshal(out, "", read); err != nil {
		return nil, locate(err, "", positions)[0]
	}
	return positions, nil
}

// loadYAML reads data with read as readYAML does, and then checks what it read with validate. The error is Errors, each
// with File set to name and the line and the column of the problem: for a missing item, those of the item that would
// have it. If data does not read, the error is that of reading it, without validation.
func loadYAML(name string, data []byte, read func(*decoder) error, validate func() error) error {
	positions, err := readYAML(data, read)
	if err != nil {
		return locate(err, name, nil)
	}
	if errs := locate(validate(), name, positions); len(errs) > 0 {
		return errs
	}
	return nil
}

// LoadThemeSettings reads data, the settings file of a theme, as Unmarshal does and validates it. The error is Errors,
// each with File set to name, such as "theme.yaml", and the line and the column of the problem; for a missing item,
// those of the item that would have it. If data does not read, the error is that of reading it, without validation.
// The settings are nil with an error.
func LoadThemeSettings(name string, data []byte) (*ThemeSettings, error) {
	s := new(ThemeSettings)
	if err := loadYAML(name, data, s.decode, func() error { return s.Validate() }); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ThemeSettings) decode(d *decoder) error {
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "defaultLanguage":
			s.DefaultLanguage, err = d.string()
		case "presets":
			err = s.Presets.decode(d)
		case "styles":
			s.Styles, err = readStrings(d)
		case "custom":
			s.PresetsOnly, err = readCustom(d)
		case "blocks":
			err = d.object(func(t string) error {
				b := ThemeBlock{Type: BlockType(t)}
				err := b.decode(d)
				s.Blocks = append(s.Blocks, b)
				return err
			})
		case "marks":
			s.HighlightVariants, err = readMarks(d)
		case "locations":
			s.Locations, err = readThemeNames(d)
		case "pageTemplates":
			s.PageTemplates, err = readThemeNames(d)
		case "imageSizes":
			err = d.object(func(name string) error {
				x := ImageSize{Name: name}
				err := x.decode(d)
				s.ImageSizes = append(s.ImageSizes, x)
				return err
			})
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
	return nil
}

func (p *Presets) decode(d *decoder) error {
	return d.object(func(key string) error {
		presets := p.kind(key)
		if presets == nil {
			return d.errorf("unknown key")
		}
		return d.object(func(name string) error {
			v, err := d.string()
			*presets = append(*presets, Preset{Name: name, Value: v})
			return err
		})
	})
}

// readCustom reads the mapping from kinds of presets to whether styles can also write values directly, and returns
// the kinds that cannot, in the order of customKinds.
func readCustom(d *decoder) ([]string, error) {
	var off []string
	err := d.object(func(kind string) error {
		switch kind {
		case "color", "fontSize", "spacing":
		case "gradient", "fontFamily", "shadow":
			return d.errorf("unknown key; %s has only presets", kind)
		default:
			return d.errorf("unknown key")
		}
		custom, err := d.bool()
		if err == nil && !custom {
			off = append(off, kind)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var only []string
	for _, kind := range customKinds {
		if slices.Contains(off, kind) {
			only = append(only, kind)
		}
	}
	return only, nil
}

func (b *ThemeBlock) decode(d *decoder) error {
	return d.object(func(key string) error {
		var err error
		switch key {
		case "styles":
			b.Styles, err = readStrings(d)
		case "variants":
			b.Variants, err = readThemeNames(d)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
}

// readMarks reads the mapping from inline marks to their settings, of which only highlight has any: its variants.
func readMarks(d *decoder) ([]ThemeName, error) {
	var variants []ThemeName
	err := d.object(func(mark string) error {
		if mark != "highlight" {
			return d.errorf("unknown key; only highlight has variants")
		}
		return d.object(func(key string) error {
			if key != "variants" {
				return d.errorf("unknown key")
			}
			var err error
			variants, err = readThemeNames(d)
			return err
		})
	})
	return variants, err
}

func (x *ImageSize) decode(d *decoder) error {
	return d.object(func(key string) error {
		var err error
		switch key {
		case "label":
			x.Label, err = d.string()
		case "width":
			x.Width, err = readInt(d, func(n int) error { return checkImageSide("width", n) })
		case "height":
			x.Height, err = readInt(d, func(n int) error { return checkImageSide("height", n) })
		case "crop":
			x.Crop, err = d.bool()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
}

// readThemeNames reads a mapping from names to their labels.
func readThemeNames(d *decoder) ([]ThemeName, error) {
	var names []ThemeName
	err := d.object(func(name string) error {
		label, err := d.string()
		names = append(names, ThemeName{Name: name, Label: label})
		return err
	})
	return names, err
}

func checkImageSide(name string, v int) error {
	if v < 1 || v > MaxImageSide {
		return fmt.Errorf("%s %d is out of range (1 to %d)", name, v, MaxImageSide)
	}
	return nil
}

// Validate checks s. Every item is optional, but the names of variants, locations, page templates and image sizes need
// labels, and image sizes need a width or a height.
func (s *ThemeSettings) Validate() error {
	var c checker
	s.check(&c)
	return c.result()
}

func (s *ThemeSettings) check(c *checker) {
	if s.DefaultLanguage != "" {
		c.add("/defaultLanguage", checkLanguage(s.DefaultLanguage))
	}
	for _, kind := range presetKinds {
		presets := *s.Presets.kind(kind)
		for i, x := range presets {
			p := "/presets/" + kind + "/" + escapePointer(x.Name)
			if err := checkName(x.Name); err != nil {
				c.add(p, err)
			} else if slices.ContainsFunc(presets[:i], func(o Preset) bool { return o.Name == x.Name }) {
				c.addf(p, "duplicate preset %q", x.Name)
			}
			c.add(p, checkCSSValue(x.Value))
		}
	}
	checkStyleItems(c, "/styles", s.Styles)
	for i, kind := range s.PresetsOnly {
		p := "/custom/" + escapePointer(kind)
		if err := checkChoice("kind", kind, customKinds...); err != nil {
			c.add(p, err)
		} else if slices.Contains(s.PresetsOnly[:i], kind) {
			c.addf(p, "duplicate kind %q", kind)
		}
	}
	for i := range s.Blocks {
		b := &s.Blocks[i]
		p := "/blocks/" + escapePointer(string(b.Type))
		spec, known := blockSpecs[b.Type]
		switch {
		case !known:
			c.addf(p, "unknown block type %q", b.Type)
		case spec.part:
			c.addf(p, "%s is a part of %s; parts have no styles or variants", b.Type, spec.parent)
		case slices.ContainsFunc(s.Blocks[:i], func(o ThemeBlock) bool { return o.Type == b.Type }):
			c.addf(p, "duplicate block type %q", b.Type)
		}
		checkStyleItems(c, p+"/styles", b.Styles)
		checkThemeNames(c, p+"/variants", "variant", b.Variants)
	}
	checkThemeNames(c, "/marks/highlight/variants", "variant", s.HighlightVariants)
	checkThemeNames(c, "/locations", "location", s.Locations)
	checkThemeNames(c, "/pageTemplates", "page template", s.PageTemplates)
	for i := range s.ImageSizes {
		x := &s.ImageSizes[i]
		p := "/imageSizes/" + escapePointer(x.Name)
		if err := checkName(x.Name); err != nil {
			c.add(p, err)
		} else if slices.ContainsFunc(s.ImageSizes[:i], func(o ImageSize) bool { return o.Name == x.Name }) {
			c.addf(p, "duplicate image size %q", x.Name)
		}
		if x.Label == "" {
			c.addf(p+"/label", "missing label")
		} else {
			c.add(p+"/label", checkLine(x.Label))
		}
		switch {
		case x.Width == 0 && x.Height == 0:
			c.addf(p, "missing width and height; set one or both")
		case x.Crop && (x.Width == 0 || x.Height == 0):
			c.addf(p+"/crop", "crop needs both width and height")
		}
		if x.Width != 0 {
			c.add(p+"/width", checkImageSide("width", x.Width))
		}
		if x.Height != 0 {
			c.add(p+"/height", checkImageSide("height", x.Height))
		}
	}
}

// checkStyleItems checks items, a list of style items at path.
func checkStyleItems(c *checker, path string, items []string) {
	for i, item := range items {
		p := path + "/" + strconv.Itoa(i)
		if err := checkChoice("style item", item, styleItems...); err != nil {
			c.add(p, err)
		} else if slices.Contains(items[:i], item) {
			c.addf(p, "duplicate style item %q", item)
		}
	}
}

// checkThemeNames checks names, of the kind what such as "variant", in the mapping at path.
func checkThemeNames(c *checker, path, what string, names []ThemeName) {
	for i, x := range names {
		p := path + "/" + escapePointer(x.Name)
		if err := checkName(x.Name); err != nil {
			c.add(p, err)
		} else if slices.ContainsFunc(names[:i], func(o ThemeName) bool { return o.Name == x.Name }) {
			c.addf(p, "duplicate %s %q", what, x.Name)
		}
		if x.Label == "" {
			c.addf(p, "missing label")
		} else {
			c.add(p, checkLine(x.Label))
		}
	}
}
