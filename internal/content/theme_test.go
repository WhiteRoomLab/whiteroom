package content

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// everyThemeYAML is a settings file of a theme with every item.
const everyThemeYAML = `# Every item of the settings file of a theme.
schemaVersion: 1
defaultLanguage: ja
presets:
  color:
    primary: "#1e73be"
    text: oklch(25% 0 0)
  fontSize:
    small: 0.875rem
    large: clamp(1.25rem, 1rem + 1vw, 1.5rem)
  spacing:
    "10": 0.25rem
    "20": calc(0.25rem * 2)
  fontFamily:
    body: '"Noto Sans JP", sans-serif'
    mono: ui-monospace, monospace
  gradient:
    sunset: "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"
  shadow:
    soft: 0 2px 4px rgba(0, 0, 0, 0.1)
styles: [textColor, backgroundColor, fontSize, padding, margin]
custom:
  color: false
  fontSize: false
  spacing: false
blocks:
  paragraph:
    variants:
      lead: リード文
  code:
    styles: [fontSize, backgroundColor]
  group:
    styles: [backgroundColor, padding, border]
    variants: { box: ボックス, note: 注意書き }
marks:
  highlight:
    variants:
      marker: マーカー
locations:
  header: ヘッダー
  footer: フッター
pageTemplates:
  landing: ランディングページ
imageSizes:
  thumbnail: { label: サムネイル, width: 150, height: 150, crop: true }
  medium: { label: 中, width: 300 }
  tall: { label: 縦長, height: 800 }
`

// everyTheme returns the settings of everyThemeYAML.
func everyTheme() *ThemeSettings {
	return &ThemeSettings{
		DefaultLanguage: "ja",
		Presets: Presets{
			Color:      []Preset{{Name: "primary", Value: "#1e73be"}, {Name: "text", Value: "oklch(25% 0 0)"}},
			FontSize:   []Preset{{Name: "small", Value: "0.875rem"}, {Name: "large", Value: "clamp(1.25rem, 1rem + 1vw, 1.5rem)"}},
			Spacing:    []Preset{{Name: "10", Value: "0.25rem"}, {Name: "20", Value: "calc(0.25rem * 2)"}},
			FontFamily: []Preset{{Name: "body", Value: `"Noto Sans JP", sans-serif`}, {Name: "mono", Value: "ui-monospace, monospace"}},
			Gradient:   []Preset{{Name: "sunset", Value: "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"}},
			Shadow:     []Preset{{Name: "soft", Value: "0 2px 4px rgba(0, 0, 0, 0.1)"}},
		},
		Styles:      []string{"textColor", "backgroundColor", "fontSize", "padding", "margin"},
		PresetsOnly: []string{"color", "fontSize", "spacing"},
		Blocks: []ThemeBlock{
			{Type: BlockParagraph, Variants: []ThemeName{{Name: "lead", Label: "リード文"}}},
			{Type: BlockCode, Styles: []string{"fontSize", "backgroundColor"}},
			{
				Type:     BlockGroup,
				Styles:   []string{"backgroundColor", "padding", "border"},
				Variants: []ThemeName{{Name: "box", Label: "ボックス"}, {Name: "note", Label: "注意書き"}},
			},
		},
		HighlightVariants: []ThemeName{{Name: "marker", Label: "マーカー"}},
		Locations:         []ThemeName{{Name: "header", Label: "ヘッダー"}, {Name: "footer", Label: "フッター"}},
		PageTemplates:     []ThemeName{{Name: "landing", Label: "ランディングページ"}},
		ImageSizes: []ImageSize{
			{Name: "thumbnail", Label: "サムネイル", Width: 150, Height: 150, Crop: true},
			{Name: "medium", Label: "中", Width: 300},
			{Name: "tall", Label: "縦長", Height: 800},
		},
	}
}

var unmarshalThemeTests = []struct {
	name  string
	in    string
	want  *ThemeSettings
	valid bool // Validate accepts it
}{
	{name: "every item", in: everyThemeYAML, want: everyTheme(), valid: true},
	{name: "schemaVersion only", in: "schemaVersion: 1\n", want: &ThemeSettings{}, valid: true},
	{
		name: "JSON",
		in:   `{"schemaVersion": 1, "locations": {"header": "ヘッダー"}, "imageSizes": {"icon": {"label": "アイコン", "width": 64, "height": 64, "crop": true}}}`,
		want: &ThemeSettings{
			Locations:  []ThemeName{{Name: "header", Label: "ヘッダー"}},
			ImageSizes: []ImageSize{{Name: "icon", Label: "アイコン", Width: 64, Height: 64, Crop: true}},
		},
		valid: true,
	},
	{
		name: "written order",
		in: `imageSizes:
  zeta: { label: Z, width: 1 }
  alpha: { height: 10000, label: A }
locations: { zeta: Z, alpha: A }
blocks:
  quote:
    variants: { zeta: Z, alpha: A }
  heading:
    styles: [margin, textColor]
presets:
  shadow: { zeta: none, alpha: 0 0 1px red }
  color: { zeta: red, alpha: blue }
custom: { spacing: false, fontSize: true, color: false }
schemaVersion: 1
`,
		want: &ThemeSettings{
			Presets: Presets{
				Color:  []Preset{{Name: "zeta", Value: "red"}, {Name: "alpha", Value: "blue"}},
				Shadow: []Preset{{Name: "zeta", Value: "none"}, {Name: "alpha", Value: "0 0 1px red"}},
			},
			PresetsOnly: []string{"color", "spacing"},
			Blocks: []ThemeBlock{
				{Type: BlockQuote, Variants: []ThemeName{{Name: "zeta", Label: "Z"}, {Name: "alpha", Label: "A"}}},
				{Type: BlockHeading, Styles: []string{"margin", "textColor"}},
			},
			Locations:  []ThemeName{{Name: "zeta", Label: "Z"}, {Name: "alpha", Label: "A"}},
			ImageSizes: []ImageSize{{Name: "zeta", Label: "Z", Width: 1}, {Name: "alpha", Label: "A", Height: 10000}},
		},
		valid: true,
	},
	{name: "custom true", in: "schemaVersion: 1\ncustom:\n  color: true\n", want: &ThemeSettings{}, valid: true},
	{
		name:  "crop false",
		in:    "schemaVersion: 1\nimageSizes:\n  large: { label: 大, width: 1024, crop: false }\n",
		want:  &ThemeSettings{ImageSizes: []ImageSize{{Name: "large", Label: "大", Width: 1024}}},
		valid: true,
	},
	{
		// Validate reports these.
		name: "values as written",
		in: `schemaVersion: 1
defaultLanguage: JA
presets:
  color:
    primary color: "red; color: blue"
styles: [shadow, shadow]
blocks:
  listItem:
    variants:
      a b: " x"
  callout:
    styles: [fontSize]
marks:
  highlight:
    variants:
      marker: Cafe` + acute + `
locations:
  "": x
imageSizes:
  medium: { crop: true }
`,
		want: &ThemeSettings{
			DefaultLanguage: "JA",
			Presets:         Presets{Color: []Preset{{Name: "primary color", Value: "red; color: blue"}}},
			Styles:          []string{"shadow", "shadow"},
			Blocks: []ThemeBlock{
				{Type: BlockListItem, Variants: []ThemeName{{Name: "a b", Label: " x"}}},
				{Type: "callout", Styles: []string{"fontSize"}},
			},
			HighlightVariants: []ThemeName{{Name: "marker", Label: "Cafe" + acute}},
			Locations:         []ThemeName{{Name: "", Label: "x"}},
			ImageSizes:        []ImageSize{{Name: "medium", Crop: true}},
		},
	},
}

func TestUnmarshalTheme(t *testing.T) {
	for _, tt := range unmarshalThemeTests {
		t.Run(tt.name, func(t *testing.T) {
			// Every item is replaced, even if in omits it.
			got := everyTheme()
			if err := got.Unmarshal([]byte(tt.in)); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", tt.in, got, tt.want)
			}
			if err := got.Validate(); (err == nil) != tt.valid {
				t.Errorf("Validate() = %v, want valid %t", err, tt.valid)
			}
		})
	}
}

// hashCommentMsg is the message of internal/yaml for a value that starts with "#" without quotes.
const hashCommentMsg = `missing value; a "#" after a space starts a comment, so quote a value that starts with "#", such as "#1e73be"`

var unmarshalThemeErrorTests = []struct {
	name         string
	in           string
	path, msg    string
	line, column int
}{
	// Errors in the YAML have no Path.
	{
		name: "duplicate key",
		in:   "schemaVersion: 1\npresets:\n  color:\n    primary: \"#1e73be\"\n    primary: \"#000\"\n",
		msg:  `duplicate key "primary"`, line: 5, column: 5,
	},
	{name: "color without quotes", in: "schemaVersion: 1\npresets:\n  color:\n    primary: #1e73be\n", msg: hashCommentMsg, line: 4, column: 5},
	{
		name: "color without quotes before another",
		in:   "schemaVersion: 1\npresets:\n  color:\n    primary: #1e73be\n    accent: \"#e85d04\"\n",
		msg:  hashCommentMsg, line: 4, column: 5,
	},
	{name: "tab", in: "schemaVersion: 1\nlocations:\n\theader: x\n", msg: "tab characters are not allowed; use spaces", line: 3, column: 1},
	{name: "empty input", in: "", msg: "empty document", line: 1, column: 1},

	// A missing key is at its parent, the root.
	{name: "missing schemaVersion", in: "presets:\n  color:\n    primary: red\n", msg: `missing key "schemaVersion"`, line: 1, column: 1},
	{
		name: "missing schemaVersion after a comment",
		in:   "# theme.yaml\n\nlocations:\n  header: ヘッダー\n",
		msg:  `missing key "schemaVersion"`, line: 3, column: 1,
	},
	{name: "schemaVersion 2", in: "schemaVersion: 2\n", path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)", line: 1, column: 1},
	{name: "schemaVersion as a string", in: "schemaVersion: \"1\"\n", path: "/schemaVersion", msg: "expected number, got string", line: 1, column: 1},

	{name: "unknown key", in: "schemaVersion: 1\nname: テーマ\n", path: "/name", msg: "unknown key", line: 2, column: 1},
	{name: "key case", in: "schemaVersion: 1\nPresets:\n  color:\n    a: red\n", path: "/Presets", msg: "unknown key", line: 2, column: 1},
	{
		name: "unknown kind of presets",
		in:   "schemaVersion: 1\npresets:\n  shadows:\n    soft: none\n",
		path: "/presets/shadows", msg: "unknown key", line: 3, column: 3,
	},
	{name: "unknown kind of custom", in: "schemaVersion: 1\ncustom:\n  border: false\n", path: "/custom/border", msg: "unknown key", line: 3, column: 3},
	{name: "style item in custom", in: "schemaVersion: 1\ncustom:\n  textColor: false\n", path: "/custom/textColor", msg: "unknown key", line: 3, column: 3},
	{
		name: "gradient in custom",
		in:   "schemaVersion: 1\ncustom:\n  color: false\n  gradient: false\n",
		path: "/custom/gradient", msg: "unknown key; gradient has only presets", line: 4, column: 3,
	},
	{
		name: "fontFamily in custom",
		in:   "schemaVersion: 1\ncustom:\n  fontFamily: true\n",
		path: "/custom/fontFamily", msg: "unknown key; fontFamily has only presets", line: 3, column: 3,
	},
	{
		name: "shadow in custom",
		in:   "schemaVersion: 1\ncustom: { shadow: false }\n",
		path: "/custom/shadow", msg: "unknown key; shadow has only presets", line: 2, column: 11,
	},
	{
		name: "unknown key in a block",
		in:   "schemaVersion: 1\nblocks:\n  code:\n    style: [fontSize]\n",
		path: "/blocks/code/style", msg: "unknown key", line: 4, column: 5,
	},
	{
		name: "mark other than highlight",
		in:   "schemaVersion: 1\nmarks:\n  bold:\n    variants:\n      heavy: 太字\n",
		path: "/marks/bold", msg: "unknown key; only highlight has variants", line: 3, column: 3,
	},
	{
		name: "unknown mark",
		in:   "schemaVersion: 1\nmarks:\n  highlight:\n    variants:\n      marker: マーカー\n  foo:\n    variants:\n      x: y\n",
		path: "/marks/foo", msg: "unknown key; only highlight has variants", line: 6, column: 3,
	},
	{
		name: "unknown key in highlight",
		in:   "schemaVersion: 1\nmarks:\n  highlight:\n    styles: [textColor]\n",
		path: "/marks/highlight/styles", msg: "unknown key", line: 4, column: 5,
	},
	{
		name: "unknown key in an image size",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail: { label: サムネイル, width: 150, size: 150 }\n",
		path: "/imageSizes/thumbnail/size", msg: "unknown key", line: 3, column: 42,
	},
	{
		name: "name of an image size",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail:\n    name: thumbnail\n",
		path: "/imageSizes/thumbnail/name", msg: "unknown key", line: 4, column: 5,
	},

	{name: "defaultLanguage as a number", in: "schemaVersion: 1\ndefaultLanguage: 1\n", path: "/defaultLanguage", msg: "expected string, got number", line: 2, column: 1},
	{name: "presets as a sequence", in: "schemaVersion: 1\npresets:\n  - color\n", path: "/presets", msg: "expected object, got array", line: 2, column: 1},
	{name: "kind as a sequence", in: "schemaVersion: 1\npresets:\n  color: [red]\n", path: "/presets/color", msg: "expected object, got array", line: 3, column: 3},
	{
		name: "preset as a number",
		in:   "schemaVersion: 1\npresets:\n  spacing:\n    \"0\": 0\n",
		path: "/presets/spacing/0", msg: "expected string, got number", line: 4, column: 5,
	},
	{
		name: "preset as a mapping",
		in:   "schemaVersion: 1\npresets:\n  color:\n    primary: { value: red }\n",
		path: "/presets/color/primary", msg: "expected string, got object", line: 4, column: 5,
	},
	{name: "styles as a string", in: "schemaVersion: 1\nstyles: textColor\n", path: "/styles", msg: "expected array, got string", line: 2, column: 1},
	{name: "style item as a number", in: "schemaVersion: 1\nstyles: [textColor, 1]\n", path: "/styles/1", msg: "expected string, got number", line: 2, column: 21},
	{name: "custom as a boolean", in: "schemaVersion: 1\ncustom: false\n", path: "/custom", msg: "expected object, got boolean", line: 2, column: 1},
	{
		name: "custom as a quoted string",
		in:   "schemaVersion: 1\ncustom:\n  color: \"false\"\n",
		path: "/custom/color", msg: "expected boolean, got string", line: 3, column: 3,
	},
	{name: "custom as no", in: "schemaVersion: 1\ncustom:\n  color: no\n", path: "/custom/color", msg: "expected boolean, got string", line: 3, column: 3},
	{name: "blocks as a sequence", in: "schemaVersion: 1\nblocks: [code]\n", path: "/blocks", msg: "expected object, got array", line: 2, column: 1},
	{name: "block as a sequence", in: "schemaVersion: 1\nblocks:\n  code: [fontSize]\n", path: "/blocks/code", msg: "expected object, got array", line: 3, column: 3},
	{
		name: "variants as a sequence",
		in:   "schemaVersion: 1\nblocks:\n  group:\n    variants: [box]\n",
		path: "/blocks/group/variants", msg: "expected object, got array", line: 4, column: 5,
	},
	{
		name: "variant as a mapping",
		in:   "schemaVersion: 1\nblocks:\n  group:\n    variants:\n      box: { label: ボックス }\n",
		path: "/blocks/group/variants/box", msg: "expected string, got object", line: 5, column: 7,
	},
	{name: "location as a number", in: "schemaVersion: 1\nlocations:\n  header: 1\n", path: "/locations/header", msg: "expected string, got number", line: 3, column: 3},
	{
		name: "page template as a boolean",
		in:   "schemaVersion: 1\npageTemplates:\n  landing: true\n",
		path: "/pageTemplates/landing", msg: "expected string, got boolean", line: 3, column: 3,
	},
	{
		name: "image size as a number",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail: 150\n",
		path: "/imageSizes/thumbnail", msg: "expected object, got number", line: 3, column: 3,
	},
	{
		name: "label of an image size as a number",
		in:   "schemaVersion: 1\nimageSizes:\n  large:\n    label: 1024\n",
		path: "/imageSizes/large/label", msg: "expected string, got number", line: 4, column: 5,
	},
	{
		name: "width as a string",
		in:   "schemaVersion: 1\nimageSizes:\n  large:\n    width: \"1024\"\n",
		path: "/imageSizes/large/width", msg: "expected number, got string", line: 4, column: 5,
	},
	{
		name: "crop as yes",
		in:   "schemaVersion: 1\nimageSizes:\n  large:\n    crop: yes\n",
		path: "/imageSizes/large/crop", msg: "expected boolean, got string", line: 4, column: 5,
	},
	{name: "sequence", in: "- schemaVersion: 1\n", msg: "expected object, got array", line: 1, column: 1},

	// Integers are checked when reading: 0 would read as omitted.
	{
		name: "width 0",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail:\n    label: サムネイル\n    width: 0\n",
		path: "/imageSizes/thumbnail/width", msg: "width 0 is out of range (1 to 10000)", line: 5, column: 5,
	},
	{
		name: "width 10001",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail:\n    label: サムネイル\n    width: 10001\n",
		path: "/imageSizes/thumbnail/width", msg: "width 10001 is out of range (1 to 10000)", line: 5, column: 5,
	},
	{
		name: "negative height",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail: { label: サムネイル, height: -150 }\n",
		path: "/imageSizes/thumbnail/height", msg: "height -150 is out of range (1 to 10000)", line: 3, column: 30,
	},
	{
		name: "width 1.5",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail:\n    width: 1.5\n",
		path: "/imageSizes/thumbnail/width", msg: "must be an integer", line: 4, column: 5,
	},
	{
		name: "width 1e3",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail:\n    width: 1e3\n",
		path: "/imageSizes/thumbnail/width", msg: "must be an integer", line: 4, column: 5,
	},

	{name: "empty mapping", in: "{}\n", msg: "empty object is not allowed", line: 1, column: 1},
	{name: "empty presets", in: "schemaVersion: 1\npresets: {}\n", path: "/presets", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty kind", in: "schemaVersion: 1\npresets:\n  color: {}\n", path: "/presets/color", msg: "empty object is not allowed", line: 3, column: 3},
	{
		name: "empty preset",
		in:   "schemaVersion: 1\npresets:\n  color:\n    primary: \"\"\n",
		path: "/presets/color/primary", msg: "empty string is not allowed", line: 4, column: 5,
	},
	{name: "empty styles", in: "schemaVersion: 1\nstyles: []\n", path: "/styles", msg: "empty array is not allowed", line: 2, column: 1},
	{name: "empty style item", in: "schemaVersion: 1\nstyles: ['']\n", path: "/styles/0", msg: "empty string is not allowed", line: 2, column: 10},
	{name: "empty custom", in: "schemaVersion: 1\ncustom: {}\n", path: "/custom", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty blocks", in: "schemaVersion: 1\nblocks: {}\n", path: "/blocks", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty block", in: "schemaVersion: 1\nblocks:\n  code: {}\n", path: "/blocks/code", msg: "empty object is not allowed", line: 3, column: 3},
	{
		name: "empty variants",
		in:   "schemaVersion: 1\nblocks:\n  group:\n    variants: {}\n",
		path: "/blocks/group/variants", msg: "empty object is not allowed", line: 4, column: 5,
	},
	{name: "empty marks", in: "schemaVersion: 1\nmarks: {}\n", path: "/marks", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty highlight", in: "schemaVersion: 1\nmarks:\n  highlight: {}\n", path: "/marks/highlight", msg: "empty object is not allowed", line: 3, column: 3},
	{name: "empty locations", in: "schemaVersion: 1\nlocations: {}\n", path: "/locations", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty label", in: "schemaVersion: 1\nlocations:\n  header: ''\n", path: "/locations/header", msg: "empty string is not allowed", line: 3, column: 3},
	{name: "empty page templates", in: "schemaVersion: 1\npageTemplates: {}\n", path: "/pageTemplates", msg: "empty object is not allowed", line: 2, column: 1},
	{name: "empty image sizes", in: "schemaVersion: 1\nimageSizes: {}\n", path: "/imageSizes", msg: "empty object is not allowed", line: 2, column: 1},
	{
		name: "empty image size",
		in:   "schemaVersion: 1\nimageSizes:\n  thumbnail: {}\n",
		path: "/imageSizes/thumbnail", msg: "empty object is not allowed", line: 3, column: 3,
	},

	// Pointers escape "~" and "/", and columns count characters.
	{
		name: "escaped name",
		in:   "schemaVersion: 1\nimageSizes:\n  \"a/b~c\":\n    size: 1\n",
		path: "/imageSizes/a~1b~0c/size", msg: "unknown key", line: 4, column: 5,
	},
	{
		name: "after Japanese",
		in:   "schemaVersion: 1\nblocks:\n  group: { variants: { 囲み: ボックス, 注意: 1 } }\n",
		path: "/blocks/group/variants/注意", msg: "expected string, got number", line: 3, column: 34,
	},
	{
		name: "error after the other items",
		in:   strings.TrimSuffix(everyThemeYAML, " }\n") + ", crop: false, size: 1 }\n",
		path: "/imageSizes/tall/size", msg: "unknown key", line: 47, column: 48,
	},
}

func TestUnmarshalThemeErrors(t *testing.T) {
	for _, tt := range unmarshalThemeErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got := everyTheme()
			err := got.Unmarshal([]byte(tt.in))
			want := &Error{Path: tt.path, Msg: tt.msg, Line: tt.line, Column: tt.column}
			var e *Error
			if !errors.As(err, &e) || *e != *want {
				t.Errorf("Unmarshal(%q) = %#v, want %#v", tt.in, err, want)
			}
			if !reflect.DeepEqual(got, everyTheme()) {
				t.Errorf("Unmarshal(%q) changed the settings to %#v", tt.in, got)
			}
		})
	}
}

// TestThemeKeys checks that everyThemeYAML and everyTheme have every item, and that every key reads its value and
// reports a value of another type at the key.
func TestThemeKeys(t *testing.T) {
	out, _, err := yaml.ToJSON([]byte(everyThemeYAML))
	if err != nil {
		t.Fatal(err)
	}
	topLevel := []string{
		"defaultLanguage", "presets", "styles", "custom", "blocks", "marks", "locations", "pageTemplates", "imageSizes",
	}
	if got, want := topKeys(t, out), append([]string{"schemaVersion"}, topLevel...); !slices.Equal(got, want) {
		t.Errorf("keys of everyThemeYAML = %q, want %q", got, want)
	}
	every := everyTheme()
	for _, v := range []reflect.Value{reflect.ValueOf(*every), reflect.ValueOf(every.Presets)} {
		for i := range v.NumField() {
			if v.Field(i).IsZero() {
				t.Errorf("everyTheme() has no %s", v.Type().Field(i).Name)
			}
		}
	}

	tests := []struct {
		head         string
		path         string
		keys         []string
		mappings     []string // the keys whose values are mappings
		line, column int
	}{
		{
			head: "schemaVersion: 1\n", keys: topLevel,
			mappings: []string{"presets", "custom", "blocks", "marks", "locations", "pageTemplates", "imageSizes"}, line: 2, column: 1,
		},
		{head: "schemaVersion: 1\npresets:\n", path: "/presets", keys: presetKinds, mappings: presetKinds, line: 3, column: 3},
		{head: "schemaVersion: 1\ncustom:\n", path: "/custom", keys: customKinds, line: 3, column: 3},
		{
			head: "schemaVersion: 1\nblocks:\n  code:\n", path: "/blocks/code",
			keys: []string{"styles", "variants"}, mappings: []string{"variants"}, line: 4, column: 5,
		},
		{head: "schemaVersion: 1\nmarks:\n", path: "/marks", keys: []string{"highlight"}, mappings: []string{"highlight"}, line: 3, column: 3},
		{
			head: "schemaVersion: 1\nmarks:\n  highlight:\n", path: "/marks/highlight",
			keys: []string{"variants"}, mappings: []string{"variants"}, line: 4, column: 5,
		},
		{
			head: "schemaVersion: 1\nimageSizes:\n  thumbnail:\n", path: "/imageSizes/thumbnail",
			keys: []string{"label", "width", "height", "crop"}, line: 4, column: 5,
		},
	}
	for _, tt := range tests {
		for _, key := range tt.keys {
			value, kind := "{ x: 1 }", "object"
			if slices.Contains(tt.mappings, key) {
				value, kind = "[x]", "array"
			}
			in := tt.head + strings.Repeat(" ", tt.column-1) + key + ": " + value + "\n"
			err := new(ThemeSettings).Unmarshal([]byte(in))
			var e *Error
			if !errors.As(err, &e) || e.Path != tt.path+"/"+key || e.Line != tt.line || e.Column != tt.column ||
				!strings.HasPrefix(e.Msg, "expected ") || !strings.HasSuffix(e.Msg, ", got "+kind) {
				t.Errorf("Unmarshal(%q) = %#v, want an error about the %s at %d:%d", in, err, kind, tt.line, tt.column)
			}
		}
	}
}

// TestThemeLists checks styleItems against the fields of Style, and presetKinds and customKinds against those of
// Presets.
func TestThemeLists(t *testing.T) {
	style := reflect.TypeFor[Style]()
	var items []string
	for i := range style.NumField() {
		name, _, _ := strings.Cut(style.Field(i).Tag.Get("json"), ",")
		items = append(items, name)
	}
	if !slices.Equal(styleItems, items) {
		t.Errorf("styleItems = %q, want the keys of Style, %q", styleItems, items)
	}

	var p Presets
	v := reflect.ValueOf(&p).Elem()
	if v.NumField() != len(presetKinds) {
		t.Fatalf("presetKinds = %q, want the %d fields of Presets", presetKinds, v.NumField())
	}
	for i, kind := range presetKinds {
		field := v.Type().Field(i).Name
		if kind != strings.ToLower(field[:1])+field[1:] || p.kind(kind) != v.Field(i).Addr().Interface() {
			t.Errorf("presetKinds[%d] = %q, want the field %s", i, kind, field)
		}
	}
	if got := p.kind("Color"); got != nil {
		t.Errorf("kind(%q) = %p, want nil", "Color", got)
	}
	if !slices.Equal(customKinds, presetKinds[:3]) {
		t.Errorf("customKinds = %q, want %q", customKinds, presetKinds[:3])
	}
}

var validateThemeTests = []struct {
	name   string
	modify func(s *ThemeSettings)
	want   []Error // Path and Msg
}{
	{name: "every item"},
	{name: "same name in two kinds", modify: func(s *ThemeSettings) { s.Presets.FontSize[0].Name = "primary" }},
	{name: "every style item", modify: func(s *ThemeSettings) { s.Styles = slices.Clone(styleItems) }},
	{
		name:   "default language",
		modify: func(s *ThemeSettings) { s.DefaultLanguage = "en_US" },
		want:   []Error{{Path: "/defaultLanguage", Msg: `invalid language code "en_US": use a language tag such as "ja", "en-US" or "zh-Hant"`}},
	},

	{
		name:   "name of a preset",
		modify: func(s *ThemeSettings) { s.Presets.Color[1].Name = "text color" },
		want:   []Error{{Path: "/presets/color/text color", Msg: `invalid name "text color": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name:   "escaped name of a preset",
		modify: func(s *ThemeSettings) { s.Presets.Spacing[0].Name = "1/2" },
		want:   []Error{{Path: "/presets/spacing/1~12", Msg: `invalid name "1/2": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name:   "empty name of a preset",
		modify: func(s *ThemeSettings) { s.Presets.Shadow[0].Name = "" },
		want:   []Error{{Path: "/presets/shadow/", Msg: "empty name"}},
	},
	{
		name: "duplicate preset",
		modify: func(s *ThemeSettings) {
			s.Presets.Color = append(s.Presets.Color, Preset{Name: "primary", Value: "#000"})
		},
		want: []Error{{Path: "/presets/color/primary", Msg: `duplicate preset "primary"`}},
	},
	{
		name:   "declaration in a preset",
		modify: func(s *ThemeSettings) { s.Presets.FontSize[0].Value = "1rem; color: red" },
		want:   []Error{{Path: "/presets/fontSize/small", Msg: `";" is not allowed; ` + cssNotRule}},
	},
	{
		name:   "url in a preset",
		modify: func(s *ThemeSettings) { s.Presets.Gradient[0].Value = "url(a.png)" },
		want:   []Error{{Path: "/presets/gradient/sunset", Msg: "url" + cssURL}},
	},
	{
		name:   "empty preset",
		modify: func(s *ThemeSettings) { s.Presets.FontFamily[1].Value = "" },
		want:   []Error{{Path: "/presets/fontFamily/mono", Msg: "empty value"}},
	},
	{
		name:   "name and value of a preset",
		modify: func(s *ThemeSettings) { s.Presets.Color[0] = Preset{Name: "a.b", Value: "red "} },
		want: []Error{
			{Path: "/presets/color/a.b", Msg: `invalid name "a.b": use ASCII letters, digits, "-" and "_"`},
			{Path: "/presets/color/a.b", Msg: cssSpace},
		},
	},

	{
		name:   "unknown style item",
		modify: func(s *ThemeSettings) { s.Styles = []string{"textColor", "shadow"} },
		want:   []Error{{Path: "/styles/1", Msg: `unknown style item "shadow" (use ` + strings.Join(styleItems, ", ") + ")"}},
	},
	{
		name:   "style item in another case",
		modify: func(s *ThemeSettings) { s.Styles = []string{"TextColor"} },
		want:   []Error{{Path: "/styles/0", Msg: `unknown style item "TextColor" (use ` + strings.Join(styleItems, ", ") + ")"}},
	},
	{
		name:   "duplicate style item",
		modify: func(s *ThemeSettings) { s.Styles = []string{"fontSize", "margin", "fontSize"} },
		want:   []Error{{Path: "/styles/2", Msg: `duplicate style item "fontSize"`}},
	},

	{
		name:   "unknown kind of presets only",
		modify: func(s *ThemeSettings) { s.PresetsOnly = []string{"color", "border"} },
		want:   []Error{{Path: "/custom/border", Msg: `unknown kind "border" (use color, fontSize, spacing)`}},
	},
	{
		name:   "kind without direct values",
		modify: func(s *ThemeSettings) { s.PresetsOnly = []string{"gradient"} },
		want:   []Error{{Path: "/custom/gradient", Msg: `unknown kind "gradient" (use color, fontSize, spacing)`}},
	},
	{
		name:   "duplicate kind",
		modify: func(s *ThemeSettings) { s.PresetsOnly = []string{"color", "color"} },
		want:   []Error{{Path: "/custom/color", Msg: `duplicate kind "color"`}},
	},

	{
		name: "unknown block type",
		modify: func(s *ThemeSettings) {
			s.Blocks = append(s.Blocks, ThemeBlock{Type: "callout", Variants: []ThemeName{{Name: "left", Label: "左"}}})
		},
		want: []Error{{Path: "/blocks/callout", Msg: `unknown block type "callout"`}},
	},
	{
		name:   "block type in another case",
		modify: func(s *ThemeSettings) { s.Blocks[0].Type = "Paragraph" },
		want:   []Error{{Path: "/blocks/Paragraph", Msg: `unknown block type "Paragraph"`}},
	},
	{
		name:   "listItem",
		modify: func(s *ThemeSettings) { s.Blocks[1].Type = BlockListItem },
		want:   []Error{{Path: "/blocks/listItem", Msg: "listItem is a part of list; parts have no styles or variants"}},
	},
	{
		name:   "tableRow",
		modify: func(s *ThemeSettings) { s.Blocks[1].Type = BlockTableRow },
		want:   []Error{{Path: "/blocks/tableRow", Msg: "tableRow is a part of table; parts have no styles or variants"}},
	},
	{
		name:   "tableCell",
		modify: func(s *ThemeSettings) { s.Blocks[1].Type = BlockTableCell },
		want:   []Error{{Path: "/blocks/tableCell", Msg: "tableCell is a part of tableRow; parts have no styles or variants"}},
	},
	{name: "button and column, which are not parts", modify: func(s *ThemeSettings) {
		s.Blocks[0].Type, s.Blocks[1].Type = BlockButton, BlockColumn
	}},
	{
		name:   "duplicate block type",
		modify: func(s *ThemeSettings) { s.Blocks[2].Type = BlockCode },
		want:   []Error{{Path: "/blocks/code", Msg: `duplicate block type "code"`}},
	},
	{
		name:   "style item of a block",
		modify: func(s *ThemeSettings) { s.Blocks[1].Styles = []string{"fontSize", "shadow", "fontSize"} },
		want: []Error{
			{Path: "/blocks/code/styles/1", Msg: `unknown style item "shadow" (use ` + strings.Join(styleItems, ", ") + ")"},
			{Path: "/blocks/code/styles/2", Msg: `duplicate style item "fontSize"`},
		},
	},
	{
		name:   "name of a variant",
		modify: func(s *ThemeSettings) { s.Blocks[2].Variants[0].Name = "box!" },
		want:   []Error{{Path: "/blocks/group/variants/box!", Msg: `invalid name "box!": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name:   "duplicate variant",
		modify: func(s *ThemeSettings) { s.Blocks[2].Variants[1].Name = "box" },
		want:   []Error{{Path: "/blocks/group/variants/box", Msg: `duplicate variant "box"`}},
	},
	{
		name:   "label not in NFC",
		modify: func(s *ThemeSettings) { s.Blocks[2].Variants[0].Label = "Cafe" + acute },
		want:   []Error{{Path: "/blocks/group/variants/box", Msg: "not in Unicode NFC; normalize it first"}},
	},
	{
		name:   "label with a tab",
		modify: func(s *ThemeSettings) { s.Blocks[0].Variants[0].Label = "リード\t文" },
		want:   []Error{{Path: "/blocks/paragraph/variants/lead", Msg: "contains a line break or tab"}},
	},
	{
		name:   "label with a control character",
		modify: func(s *ThemeSettings) { s.HighlightVariants[0].Label = "マーカー" + string(rune(0x0001)) },
		want:   []Error{{Path: "/marks/highlight/variants/marker", Msg: "contains control characters; normalize it first"}},
	},
	{
		name:   "missing label of a variant",
		modify: func(s *ThemeSettings) { s.Blocks[2].Variants[1].Label = "" },
		want:   []Error{{Path: "/blocks/group/variants/note", Msg: "missing label"}},
	},
	{
		name:   "name of a highlight variant",
		modify: func(s *ThemeSettings) { s.HighlightVariants[0].Name = "a b" },
		want:   []Error{{Path: "/marks/highlight/variants/a b", Msg: `invalid name "a b": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name: "duplicate highlight variant",
		modify: func(s *ThemeSettings) {
			s.HighlightVariants = append(s.HighlightVariants, ThemeName{Name: "marker", Label: "黄色"})
		},
		want: []Error{{Path: "/marks/highlight/variants/marker", Msg: `duplicate variant "marker"`}},
	},
	{
		name:   "name of a location",
		modify: func(s *ThemeSettings) { s.Locations[1].Name = "フッター" },
		want:   []Error{{Path: "/locations/フッター", Msg: `invalid name "フッター": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name:   "duplicate location",
		modify: func(s *ThemeSettings) { s.Locations[1].Name = "header" },
		want:   []Error{{Path: "/locations/header", Msg: `duplicate location "header"`}},
	},
	{
		name:   "label of a location with a line break",
		modify: func(s *ThemeSettings) { s.Locations[0].Label = "ヘッ\nダー" },
		want:   []Error{{Path: "/locations/header", Msg: "contains a line break or tab"}},
	},
	{
		name:   "missing label of a location",
		modify: func(s *ThemeSettings) { s.Locations[0].Label = "" },
		want:   []Error{{Path: "/locations/header", Msg: "missing label"}},
	},
	{
		name:   "name of a page template",
		modify: func(s *ThemeSettings) { s.PageTemplates[0].Name = "landing.page" },
		want:   []Error{{Path: "/pageTemplates/landing.page", Msg: `invalid name "landing.page": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name: "duplicate page template",
		modify: func(s *ThemeSettings) {
			s.PageTemplates = append(s.PageTemplates, ThemeName{Name: "landing", Label: "LP"})
		},
		want: []Error{{Path: "/pageTemplates/landing", Msg: `duplicate page template "landing"`}},
	},

	{
		name:   "name of an image size",
		modify: func(s *ThemeSettings) { s.ImageSizes[0].Name = "サムネイル" },
		want:   []Error{{Path: "/imageSizes/サムネイル", Msg: `invalid name "サムネイル": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name:   "duplicate image size",
		modify: func(s *ThemeSettings) { s.ImageSizes[2].Name = "thumbnail" },
		want:   []Error{{Path: "/imageSizes/thumbnail", Msg: `duplicate image size "thumbnail"`}},
	},
	{
		name:   "missing label of an image size",
		modify: func(s *ThemeSettings) { s.ImageSizes[1].Label = "" },
		want:   []Error{{Path: "/imageSizes/medium/label", Msg: "missing label"}},
	},
	{
		name:   "label of an image size with a tab",
		modify: func(s *ThemeSettings) { s.ImageSizes[1].Label = "中\t" },
		want:   []Error{{Path: "/imageSizes/medium/label", Msg: "contains a line break or tab"}},
	},
	{
		name:   "image size without width and height",
		modify: func(s *ThemeSettings) { s.ImageSizes[1].Width = 0 },
		want:   []Error{{Path: "/imageSizes/medium", Msg: "missing width and height; set one or both"}},
	},
	{
		name:   "crop with only width",
		modify: func(s *ThemeSettings) { s.ImageSizes[1].Crop = true },
		want:   []Error{{Path: "/imageSizes/medium/crop", Msg: "crop needs both width and height"}},
	},
	{
		name:   "crop with only height",
		modify: func(s *ThemeSettings) { s.ImageSizes[2].Crop = true },
		want:   []Error{{Path: "/imageSizes/tall/crop", Msg: "crop needs both width and height"}},
	},
	{
		name:   "crop without width and height",
		modify: func(s *ThemeSettings) { s.ImageSizes[0].Width, s.ImageSizes[0].Height = 0, 0 },
		want:   []Error{{Path: "/imageSizes/thumbnail", Msg: "missing width and height; set one or both"}},
	},
	{
		name:   "width out of range",
		modify: func(s *ThemeSettings) { s.ImageSizes[0].Width = MaxImageSide + 1 },
		want:   []Error{{Path: "/imageSizes/thumbnail/width", Msg: "width 10001 is out of range (1 to 10000)"}},
	},
	{
		name:   "negative height",
		modify: func(s *ThemeSettings) { s.ImageSizes[2].Height = -800 },
		want:   []Error{{Path: "/imageSizes/tall/height", Msg: "height -800 is out of range (1 to 10000)"}},
	},
	{name: "largest size", modify: func(s *ThemeSettings) { s.ImageSizes[0].Width, s.ImageSizes[0].Height = MaxImageSide, MaxImageSide }},

	{
		// In the order of ThemeSettings, not of the file.
		name: "errors in order",
		modify: func(s *ThemeSettings) {
			s.ImageSizes[0].Label = ""
			s.PageTemplates[0].Name = "a b"
			s.Locations[0].Label = ""
			s.HighlightVariants[0].Name = ""
			s.Blocks[0].Type = "p"
			s.PresetsOnly = []string{"shadow"}
			s.Styles = []string{"x"}
			s.Presets.Shadow[0].Value = "none;"
			s.Presets.Color[0].Value = "red;"
			s.DefaultLanguage = "japanese"
		},
		want: []Error{
			{Path: "/defaultLanguage", Msg: `invalid language code "japanese": use a language tag such as "ja", "en-US" or "zh-Hant"`},
			{Path: "/presets/color/primary", Msg: `";" is not allowed; ` + cssNotRule},
			{Path: "/presets/shadow/soft", Msg: `";" is not allowed; ` + cssNotRule},
			{Path: "/styles/0", Msg: `unknown style item "x" (use ` + strings.Join(styleItems, ", ") + ")"},
			{Path: "/custom/shadow", Msg: `unknown kind "shadow" (use color, fontSize, spacing)`},
			{Path: "/blocks/p", Msg: `unknown block type "p"`},
			{Path: "/marks/highlight/variants/", Msg: "empty name"},
			{Path: "/locations/header", Msg: "missing label"},
			{Path: "/pageTemplates/a b", Msg: `invalid name "a b": use ASCII letters, digits, "-" and "_"`},
			{Path: "/imageSizes/thumbnail/label", Msg: "missing label"},
		},
	},
}

func TestValidateTheme(t *testing.T) {
	for _, tt := range validateThemeTests {
		t.Run(tt.name, func(t *testing.T) {
			s := everyTheme()
			if tt.modify != nil {
				tt.modify(s)
			}
			err := s.Validate()
			var es Errors
			if err != nil && !errors.As(err, &es) {
				t.Fatalf("Validate() = %v (%T), want Errors", err, err)
			}
			var got []Error
			for _, e := range es {
				got = append(got, *e)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Validate() = %v, want %v", got, tt.want)
			}
		})
	}
}

const themeFile = "theme.yaml"

func TestLoadThemeSettings(t *testing.T) {
	got, err := LoadThemeSettings(themeFile, []byte(everyThemeYAML))
	if err != nil {
		t.Fatalf("LoadThemeSettings() = %v", err)
	}
	if !reflect.DeepEqual(got, everyTheme()) {
		t.Errorf("LoadThemeSettings() = %#v, want %#v", got, everyTheme())
	}
}

var loadThemeSettingsErrorTests = []struct {
	name string
	in   string
	want string // Error() of the error
}{
	// Errors of reading the file, without validation.
	{name: "empty file", in: "", want: "theme.yaml:1:1: empty document"},
	{
		name: "color without quotes",
		in:   "schemaVersion: 1\npresets:\n  color:\n    primary: #1e73be\n",
		want: "theme.yaml:4:5: " + hashCommentMsg,
	},
	{
		name: "preset as a number",
		in:   "schemaVersion: 1\npresets:\n  spacing:\n    \"0\": 0\n",
		want: "theme.yaml:4:5: /presets/spacing/0: expected string, got number",
	},
	{
		name: "missing schemaVersion",
		in:   "# theme.yaml\nlocations:\n  header: ヘッダー\n",
		want: `theme.yaml:2:1: missing key "schemaVersion"`,
	},
	{
		// The invalid language code is not validated.
		name: "error of reading after an invalid value",
		in:   "schemaVersion: 1\ndefaultLanguage: JA\nlocations: []\n",
		want: "theme.yaml:3:1: /locations: expected object, got array",
	},

	// Errors of Validate.
	{
		name: "declaration in a preset",
		in:   "schemaVersion: 1\npresets:\n  color:\n    primary: \"red; color: blue\"\n",
		want: `theme.yaml:4:5: /presets/color/primary: ";" is not allowed; ` + cssNotRule,
	},
	{
		name: "gradient cut short by a comment",
		in:   "schemaVersion: 1\npresets:\n  gradient:\n    sunset: linear-gradient(135deg, #f093fb 0%, #f5576c 100%)\n",
		want: `theme.yaml:4:5: /presets/gradient/sunset: unclosed "("` + cssCommentCut,
	},
	{
		name: "unknown style item",
		in:   "schemaVersion: 1\nstyles: [textColor, shadow]\n",
		want: `theme.yaml:2:21: /styles/1: unknown style item "shadow" (use ` + strings.Join(styleItems, ", ") + ")",
	},
	{
		name: "part",
		in:   "schemaVersion: 1\nblocks:\n  tableCell:\n    styles: [textColor]\n",
		want: "theme.yaml:3:3: /blocks/tableCell: tableCell is a part of tableRow; parts have no styles or variants",
	},
	{
		name: "style item of a block",
		in:   "schemaVersion: 1\nblocks:\n  code:\n    styles: [fontSize, fontSize]\n",
		want: `theme.yaml:4:24: /blocks/code/styles/1: duplicate style item "fontSize"`,
	},
	{
		name: "label not in NFC",
		in:   "schemaVersion: 1\nlocations:\n  header: ヘッダー\n  footer: Cafe" + acute + "\n",
		want: "theme.yaml:4:3: /locations/footer: not in Unicode NFC; normalize it first",
	},
	{
		// A missing item is at the item that would have it.
		name: "missing label of an image size",
		in:   "schemaVersion: 1\nimageSizes:\n  medium:\n    width: 300\n",
		want: "theme.yaml:3:3: /imageSizes/medium/label: missing label",
	},
	{
		name: "crop with only width",
		in:   "schemaVersion: 1\nimageSizes:\n  medium: { label: 中, width: 300, crop: true }\n",
		want: "theme.yaml:3:35: /imageSizes/medium/crop: crop needs both width and height",
	},
	{
		name: "escaped name",
		in:   "schemaVersion: 1\npageTemplates:\n  \"a/b\": x\n",
		want: `theme.yaml:3:3: /pageTemplates/a~1b: invalid name "a/b": use ASCII letters, digits, "-" and "_"`,
	},
	{
		name: "errors in the order of Validate",
		in:   "schemaVersion: 1\nimageSizes:\n  medium: { label: 中 }\nlocations:\n  a b: x\ndefaultLanguage: JA\n",
		want: `theme.yaml:6:1: /defaultLanguage: invalid language code "JA": use a language tag such as "ja", "en-US" or "zh-Hant"` + "\n" +
			`theme.yaml:5:3: /locations/a b: invalid name "a b": use ASCII letters, digits, "-" and "_"` + "\n" +
			"theme.yaml:3:3: /imageSizes/medium: missing width and height; set one or both",
	},
}

func TestLoadThemeSettingsErrors(t *testing.T) {
	for _, tt := range loadThemeSettingsErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadThemeSettings(themeFile, []byte(tt.in))
			var es Errors
			if !errors.As(err, &es) || err.Error() != tt.want {
				t.Errorf("LoadThemeSettings() = %q (%T), want %q", err, err, tt.want)
			}
			if got != nil {
				t.Errorf("LoadThemeSettings() = %#v with an error", got)
			}
		})
	}
}

// FuzzLoadThemeSettings checks that LoadThemeSettings returns the settings that Unmarshal reads and Validate accepts,
// or nil and the error of Unmarshal or the errors of Validate, each with its file and a position in it.
func FuzzLoadThemeSettings(f *testing.F) {
	for _, tt := range unmarshalThemeTests {
		f.Add([]byte(tt.in))
	}
	for _, tt := range unmarshalThemeErrorTests {
		f.Add([]byte(tt.in))
	}
	for _, tt := range loadThemeSettingsErrorTests {
		f.Add([]byte(tt.in))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := LoadThemeSettings(themeFile, data)
		read := everyTheme()
		readErr := read.Unmarshal(data)
		if readErr != nil && !reflect.DeepEqual(read, everyTheme()) {
			t.Fatalf("Unmarshal(%q) = %v and changed the settings to %#v", data, readErr, read)
		}
		if err == nil {
			if s == nil || readErr != nil || !reflect.DeepEqual(s, read) {
				t.Fatalf("LoadThemeSettings(%q) = %#v, but Unmarshal = %#v, %v", data, s, read, readErr)
			}
			if err := s.Validate(); err != nil {
				t.Fatalf("Validate() of the settings of LoadThemeSettings(%q) = %v", data, err)
			}
			return
		}
		var es Errors
		if s != nil || !errors.As(err, &es) || len(es) == 0 {
			t.Fatalf("LoadThemeSettings(%q) = %#v, %v (%T); want nil and Errors", data, s, err, err)
		}
		lines := strings.Split(string(data), "\n")
		for _, e := range es {
			if e.File != themeFile || e.Line < 1 || e.Line > len(lines) || e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
				t.Errorf("LoadThemeSettings(%q) = %v, outside the file", data, e)
			}
		}
		if readErr != nil {
			var re *Error
			if !errors.As(readErr, &re) || len(es) != 1 || es[0].Path != re.Path || es[0].Msg != re.Msg ||
				es[0].Line != re.Line || es[0].Column != re.Column {
				t.Errorf("LoadThemeSettings(%q) = %v, but Unmarshal = %v", data, err, readErr)
			}
			return
		}
		var want Errors
		if !errors.As(read.Validate(), &want) || len(es) != len(want) {
			t.Fatalf("LoadThemeSettings(%q) = %v, but Validate() = %v", data, err, want)
		}
		for i, e := range es {
			if e.Path != want[i].Path || e.Msg != want[i].Msg {
				t.Errorf("LoadThemeSettings(%q) = %v, but Validate() = %v", data, e, want[i])
			}
		}
	})
}
