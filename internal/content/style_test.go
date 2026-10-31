package content

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// assertRoundTrip checks that in reads as a T that writes as want (ignoring its spaces) and reads back the same.
func assertRoundTrip[T any](t *testing.T, in, want string) {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", in, err)
	}
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%#v) = %v", v, err)
	}
	canonical := jsontext.Value(want)
	if err := canonical.Compact(); err != nil {
		t.Fatalf("Compact(%s) = %v", want, err)
	}
	if !bytes.Equal(got, canonical) {
		t.Errorf("Marshal(Unmarshal(%s)) = %s, want %s", in, got, canonical)
	}
	var again T
	if err := json.Unmarshal(got, &again); err != nil {
		t.Fatalf("Unmarshal(%s) = %v", got, err)
	}
	if !reflect.DeepEqual(again, v) {
		t.Errorf("Unmarshal(%s) = %#v, want %#v", got, again, v)
	}
}

// conceptStyleJSON is the style of the block example in CONCEPT.md 6.5.1.
const conceptStyleJSON = `{
  "textColor": { "preset": "primary" },
  "backgroundColor": { "value": "#fff4e5" },
  "fontSize": { "preset": "large" },
  "padding": { "top": { "value": "1rem" }, "bottom": { "value": "1rem" } }
}`

// fullStyleJSON is fullStyle, which sets every item, in canonical form.
const fullStyleJSON = `{
  "textColor": {"preset": "primary"},
  "backgroundColor": {"value": "#fff4e5"},
  "linkColor": {"value": "rgb(30, 115, 190)"},
  "gradient": {"preset": "sunset"},
  "fontSize": {"value": "1.25rem"},
  "letterSpacing": {"value": "0.05em"},
  "fontFamily": {"preset": "serif"},
  "fontWeight": {"value": 700},
  "lineHeight": {"value": 1.5},
  "padding": {"top": {"value": "1rem"}, "right": {"preset": "medium"}, "bottom": {"value": "2rem"}, "left": {"value": "5%"}},
  "margin": {"top": {"value": "-1rem"}, "bottom": {"preset": "large"}},
  "blockGap": {"preset": "small"},
  "border": {"width": {"value": "1px"}, "style": {"value": "solid"}, "color": {"preset": "accent"}, "radius": {"value": "4px"}}
}`

var fullStyle = Style{
	TextColor:       Color{Preset: "primary"},
	BackgroundColor: Color{Value: "#fff4e5"},
	LinkColor:       Color{Value: "rgb(30, 115, 190)"},
	Gradient:        Gradient{Preset: "sunset"},
	FontSize:        Length{Value: "1.25rem"},
	LetterSpacing:   Length{Value: "0.05em"},
	FontFamily:      FontFamily{Preset: "serif"},
	FontWeight:      FontWeight{Value: 700},
	LineHeight:      LineHeight{Value: 1.5},
	Padding: Sides{
		Top:    Length{Value: "1rem"},
		Right:  Length{Preset: "medium"},
		Bottom: Length{Value: "2rem"},
		Left:   Length{Value: "5%"},
	},
	Margin:   Sides{Top: Length{Value: "-1rem"}, Bottom: Length{Preset: "large"}},
	BlockGap: Length{Preset: "small"},
	Border: Border{
		Width:  Length{Value: "1px"},
		Style:  BorderStyle{Value: "solid"},
		Color:  Color{Preset: "accent"},
		Radius: Length{Value: "4px"},
	},
}

func TestStyleUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Style
	}{
		{
			name: "concept example",
			in:   conceptStyleJSON,
			want: Style{
				TextColor:       Color{Preset: "primary"},
				BackgroundColor: Color{Value: "#fff4e5"},
				FontSize:        Length{Preset: "large"},
				Padding:         Sides{Top: Length{Value: "1rem"}, Bottom: Length{Value: "1rem"}},
			},
		},
		{name: "every item", in: fullStyleJSON, want: fullStyle},
		{name: "color format not checked", in: `{"textColor":{"value":"calc(1)"}}`, want: Style{TextColor: Color{Value: "calc(1)"}}},
		{name: "both preset and value", in: `{"fontSize":{"preset":"large","value":"1rem"}}`, want: Style{FontSize: Length{Preset: "large", Value: "1rem"}}},
		{name: "preset where not allowed", in: `{"letterSpacing":{"preset":"wide"}}`, want: Style{LetterSpacing: Length{Preset: "wide"}}},
		{name: "unknown border style", in: `{"border":{"style":{"value":"double"}}}`, want: Style{Border: Border{Style: BorderStyle{Value: "double"}}}},
		{name: "minimum font weight", in: `{"fontWeight":{"value":100}}`, want: Style{FontWeight: FontWeight{Value: 100}}},
		{name: "maximum font weight", in: `{"fontWeight":{"value":900}}`, want: Style{FontWeight: FontWeight{Value: 900}}},
		{name: "maximum line height", in: `{"lineHeight":{"value":10}}`, want: Style{LineHeight: LineHeight{Value: 10}}},
		{name: "line height with exponent", in: `{"lineHeight":{"value":15e-1}}`, want: Style{LineHeight: LineHeight{Value: 1.5}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Style
			if err := got.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnmarshalJSON(%s) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestStyleUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty object", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "null", in: `null`, path: "", msg: "expected object, got null"},
		{name: "item key case", in: `{"TextColor":{"value":"#fff"}}`, path: "/TextColor", msg: "unknown key"},
		{name: "value key case", in: `{"textColor":{"Value":"#fff"}}`, path: "/textColor/Value", msg: "unknown key"},
		{name: "side key case", in: `{"padding":{"Top":{"value":"1rem"}}}`, path: "/padding/Top", msg: "unknown key"},
		{name: "border part key case", in: `{"border":{"Color":{"value":"#fff"}}}`, path: "/border/Color", msg: "unknown key"},
		{name: "unknown key after an item", in: `{"fontSize":{"value":"1rem"},"x":1}`, path: "/x", msg: "unknown key"},
		{name: "unknown key in a side", in: `{"margin":{"left":{"value":"1rem","x":1}}}`, path: "/margin/left/x", msg: "unknown key"},
		{name: "duplicate key", in: `{"textColor":{"value":"#fff"},"textColor":{"value":"#000"}}`, path: "/textColor", msg: "duplicate key"},
		{name: "null item", in: `{"textColor":null}`, path: "/textColor", msg: "expected object, got null"},
		{name: "null value", in: `{"textColor":{"value":null}}`, path: "/textColor/value", msg: "expected string, got null"},
		{name: "string for item", in: `{"textColor":"#fff"}`, path: "/textColor", msg: "expected object, got string"},
		{name: "empty item", in: `{"textColor":{}}`, path: "/textColor", msg: "empty object is not allowed"},
		{name: "empty sides", in: `{"padding":{}}`, path: "/padding", msg: "empty object is not allowed"},
		{name: "empty string", in: `{"textColor":{"value":""}}`, path: "/textColor/value", msg: "empty string is not allowed"},
		{name: "gradient value", in: `{"gradient":{"value":"linear-gradient(#fff, #000)"}}`, path: "/gradient/value", msg: "unknown key"},
		{name: "font family value", in: `{"fontFamily":{"value":"serif"}}`, path: "/fontFamily/value", msg: "unknown key"},
		{name: "font weight preset", in: `{"fontWeight":{"preset":"bold"}}`, path: "/fontWeight/preset", msg: "unknown key"},
		{name: "border style preset", in: `{"border":{"style":{"preset":"solid"}}}`, path: "/border/style/preset", msg: "unknown key"},
		{name: "shadow", in: `{"shadow":{"preset":"soft"}}`, path: "/shadow", msg: "unknown key"},
		{name: "font style", in: `{"fontStyle":{"value":"italic"}}`, path: "/fontStyle", msg: "unknown key"},
		{name: "min height", in: `{"minHeight":{"value":"50vh"}}`, path: "/minHeight", msg: "unknown key"},
		{name: "font weight string", in: `{"fontWeight":{"value":"700"}}`, path: "/fontWeight/value", msg: "expected number, got string"},
		{name: "font weight fraction", in: `{"fontWeight":{"value":700.0}}`, path: "/fontWeight/value", msg: "must be an integer"},
		{name: "font weight exponent", in: `{"fontWeight":{"value":7e2}}`, path: "/fontWeight/value", msg: "must be an integer"},
		{name: "font weight zero", in: `{"fontWeight":{"value":0}}`, path: "/fontWeight/value", msg: "font weight 0 is out of range"},
		{name: "font weight over 900", in: `{"fontWeight":{"value":950}}`, path: "/fontWeight/value", msg: "font weight 950 is out of range"},
		{name: "line height string", in: `{"lineHeight":{"value":"1.5"}}`, path: "/lineHeight/value", msg: "expected number, got string"},
		{name: "line height zero", in: `{"lineHeight":{"value":0}}`, path: "/lineHeight/value", msg: "line height 0 is out of range"},
		{name: "line height negative zero", in: `{"lineHeight":{"value":-0}}`, path: "/lineHeight/value", msg: "line height -0 is out of range"},
		{name: "line height over 10", in: `{"lineHeight":{"value":10.5}}`, path: "/lineHeight/value", msg: "line height 10.5 is out of range"},
		{name: "line height underflow", in: `{"lineHeight":{"value":1e-400}}`, path: "/lineHeight/value", msg: "line height 0 is out of range"},
		{name: "line height overflow", in: `{"lineHeight":{"value":1e400}}`, path: "/lineHeight/value", msg: "invalid number"},
		{name: "string for border style", in: `{"border":{"style":"solid"}}`, path: "/border/style", msg: "expected object, got string"},
		{name: "trailing data", in: `{"fontSize":{"value":"1rem"}} {}`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := Style{TextColor: Color{Preset: "primary"}}
			s := orig
			assertError(t, s.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(s, orig) {
				t.Errorf("UnmarshalJSON(%s) changed the value to %#v", tt.in, s)
			}
		})
	}
}

func TestJSONUnmarshalStyleTypes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		in   string
		path string
	}{
		{name: "Style", v: new(Style), in: `{"x":1}`, path: "/x"},
		{name: "Color", v: new(Color), in: `{"Value":"#fff"}`, path: "/Value"},
		{name: "Length", v: new(Length), in: `{"Value":"1rem"}`, path: "/Value"},
		{name: "Gradient", v: new(Gradient), in: `{"value":"x"}`, path: "/value"},
		{name: "FontFamily", v: new(FontFamily), in: `{"value":"serif"}`, path: "/value"},
		{name: "FontWeight", v: new(FontWeight), in: `{"Value":700}`, path: "/Value"},
		{name: "LineHeight", v: new(LineHeight), in: `{"Value":1.5}`, path: "/Value"},
		{name: "BorderStyle", v: new(BorderStyle), in: `{"Value":"solid"}`, path: "/Value"},
		{name: "Sides", v: new(Sides), in: `{"Top":{"value":"1rem"}}`, path: "/Top"},
		{name: "Border", v: new(Border), in: `{"Width":{"value":"1px"}}`, path: "/Width"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, json.Unmarshal([]byte(tt.in), tt.v), tt.path, "unknown key")
		})
	}
}

func TestStyleRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "concept example", in: conceptStyleJSON},
		{name: "every item", in: fullStyleJSON},
		{
			name: "reversed key order",
			in: `{
  "border": {"radius": {"value": "4px"}, "color": {"preset": "accent"}, "style": {"value": "solid"}, "width": {"value": "1px"}},
  "blockGap": {"preset": "small"},
  "margin": {"bottom": {"preset": "large"}, "top": {"value": "-1rem"}},
  "padding": {"left": {"value": "5%"}, "bottom": {"value": "2rem"}, "right": {"preset": "medium"}, "top": {"value": "1rem"}},
  "lineHeight": {"value": 1.5},
  "fontWeight": {"value": 700},
  "fontFamily": {"preset": "serif"},
  "letterSpacing": {"value": "0.05em"},
  "fontSize": {"value": "1.25rem"},
  "gradient": {"preset": "sunset"},
  "linkColor": {"value": "rgb(30, 115, 190)"},
  "backgroundColor": {"value": "#fff4e5"},
  "textColor": {"preset": "primary"}
}`,
			want: fullStyleJSON,
		},
		{name: "trailing zero", in: `{"lineHeight":{"value":1.50}}`, want: `{"lineHeight":{"value":1.5}}`},
		{name: "exponent", in: `{"lineHeight":{"value":2E0}}`, want: `{"lineHeight":{"value":2}}`},
		{name: "escaped string", in: `{"textColor":{"preset":"pr\u0069mary"}}`, want: `{"textColor":{"preset":"primary"}}`},
		{name: "escaped key", in: `{"text\u0043olor":{"preset":"primary"}}`, want: `{"textColor":{"preset":"primary"}}`},
		{
			name: "spaces and newlines",
			in:   "\r\n{\n\t\"textColor\" : { \"preset\" : \"primary\" } ,\n\t\"fontWeight\":{\"value\":700}\n}\n",
			want: `{"textColor":{"preset":"primary"},"fontWeight":{"value":700}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Style](t, tt.in, want)
		})
	}
}

// assertStyleValidate checks the paths of the problems that Validate finds in s and, if msg is not "", their messages.
// It also checks that s.check(c, "/style") reports the same paths under "/style".
func assertStyleValidate(t *testing.T, s Style, paths []string, msg string) {
	t.Helper()
	err := s.Validate()
	if paths == nil && err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if got := errorPaths(t, err); !reflect.DeepEqual(got, paths) {
		t.Errorf("Validate() paths = %q, want %q", got, paths)
	}
	if msg != "" {
		assertErrorMsg(t, err, msg)
	}
	var c checker
	s.check(&c, "/style")
	var want []string
	for _, p := range paths {
		want = append(want, "/style"+p)
	}
	if got := errorPaths(t, c.result()); !reflect.DeepEqual(got, want) {
		t.Errorf(`check(c, "/style") paths = %q, want %q`, got, want)
	}
}

func TestStyleValidate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		paths []string
		msg   string // part of the messages; not checked if ""
	}{
		{name: "concept example", in: conceptStyleJSON},
		{name: "every item", in: fullStyleJSON},
		{name: "font size percentage", in: `{"fontSize":{"value":"120%"}}`},
		{name: "negative letter spacing", in: `{"letterSpacing":{"value":"-0.05em"}}`},
		{name: "negative and percentage margins", in: `{"margin":{"top":{"value":"-1rem"},"left":{"value":"5%"}}}`},
		{name: "block gap percentage", in: `{"blockGap":{"value":"10%"}}`},
		{name: "border radius percentage", in: `{"border":{"radius":{"value":"50%"}}}`},
		{name: "dashed border", in: `{"border":{"style":{"value":"dashed"}}}`},
		{name: "dotted border", in: `{"border":{"style":{"value":"dotted"}}}`},

		{name: "named color", in: `{"textColor":{"value":"red"}}`, paths: []string{"/textColor/value"}, msg: `invalid color "red"`},
		{name: "calc color", in: `{"textColor":{"value":"calc(1)"}}`, paths: []string{"/textColor/value"}},
		{name: "var color", in: `{"textColor":{"value":"var(--x)"}}`, paths: []string{"/textColor/value"}},
		{name: "url color", in: `{"textColor":{"value":"url(x)"}}`, paths: []string{"/textColor/value"}},
		{
			name:  "color preset and value",
			in:    `{"textColor":{"preset":"primary","value":"#fff"}}`,
			paths: []string{"/textColor"},
			msg:   "set either preset or value, not both",
		},
		{
			name:  "color preset with space",
			in:    `{"textColor":{"preset":"primary color"}}`,
			paths: []string{"/textColor/preset"},
			msg:   `invalid name "primary color"`,
		},
		{name: "gradient preset with space", in: `{"gradient":{"preset":"a b"}}`, paths: []string{"/gradient/preset"}},
		{
			name:  "calc font size",
			in:    `{"fontSize":{"value":"calc(1px + 2px)"}}`,
			paths: []string{"/fontSize/value"},
			msg:   `invalid length "calc(1px + 2px)"`,
		},
		{
			name:  "negative font size",
			in:    `{"fontSize":{"value":"-1rem"}}`,
			paths: []string{"/fontSize/value"},
			msg:   `negative length "-1rem" is not allowed here`,
		},
		{
			name:  "letter spacing preset",
			in:    `{"letterSpacing":{"preset":"wide"}}`,
			paths: []string{"/letterSpacing/preset"},
			msg:   "presets are not allowed here",
		},
		{
			name:  "letter spacing percentage",
			in:    `{"letterSpacing":{"value":"5%"}}`,
			paths: []string{"/letterSpacing/value"},
			msg:   `percentage "5%" is not allowed here`,
		},
		{name: "negative padding", in: `{"padding":{"top":{"value":"-1rem"}}}`, paths: []string{"/padding/top/value"}, msg: "negative length"},
		{name: "padding preset with space", in: `{"padding":{"left":{"preset":"x y"}}}`, paths: []string{"/padding/left/preset"}},
		{name: "unitless block gap", in: `{"blockGap":{"value":"1"}}`, paths: []string{"/blockGap/value"}, msg: `invalid length "1"`},
		{
			name:  "border width percentage",
			in:    `{"border":{"width":{"value":"10%"}}}`,
			paths: []string{"/border/width/value"},
			msg:   `percentage "10%" is not allowed here`,
		},
		{
			name:  "border width preset",
			in:    `{"border":{"width":{"preset":"thin"}}}`,
			paths: []string{"/border/width/preset"},
			msg:   "presets are not allowed here",
		},
		{name: "border radius preset", in: `{"border":{"radius":{"preset":"round"}}}`, paths: []string{"/border/radius/preset"}},
		{
			name:  "unknown border style",
			in:    `{"border":{"style":{"value":"double"}}}`,
			paths: []string{"/border/style/value"},
			msg:   `unknown border style "double"`,
		},
		{
			name:  "invalid border color",
			in:    `{"border":{"color":{"value":"#ggg"}}}`,
			paths: []string{"/border/color/value"},
			msg:   `invalid color "#ggg"`,
		},
		{
			name:  "negative lengths where not allowed",
			in:    `{"blockGap":{"value":"-1rem"},"border":{"width":{"value":"-1px"},"radius":{"value":"-4px"}}}`,
			paths: []string{"/blockGap/value", "/border/width/value", "/border/radius/value"},
			msg:   "negative length",
		},
		{
			name:  "preset and value both invalid",
			in:    `{"fontSize":{"preset":"a b","value":"1"}}`,
			paths: []string{"/fontSize", "/fontSize/preset", "/fontSize/value"},
		},
		{
			name:  "preset name not checked where not allowed",
			in:    `{"letterSpacing":{"preset":"a b","value":"1px"}}`,
			paths: []string{"/letterSpacing", "/letterSpacing/preset"},
			msg:   "presets are not allowed here",
		},
		{
			name:  "errors in field order, not key order",
			in:    `{"border":{"style":{"value":"double"}},"padding":{"top":{"value":"-1rem"}},"textColor":{"value":"red"}}`,
			paths: []string{"/textColor/value", "/padding/top/value", "/border/style/value"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Style
			if err := s.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", tt.in, err)
			}
			assertStyleValidate(t, s, tt.paths, tt.msg)
		})
	}
}

// TestStyleValidateGoValues checks values built in Go, some of which UnmarshalJSON rejects.
func TestStyleValidateGoValues(t *testing.T) {
	tests := []struct {
		name  string
		in    Style
		paths []string
		msg   string // part of the messages; not checked if ""
	}{
		{name: "zero", in: Style{}},
		{name: "every item", in: fullStyle},
		{name: "negative zero line height as unset", in: Style{LineHeight: LineHeight{Value: math.Copysign(0, -1)}}},
		{
			name:  "font weight below range",
			in:    Style{FontWeight: FontWeight{Value: 50}},
			paths: []string{"/fontWeight/value"},
			msg:   "font weight 50 is out of range (100-900)",
		},
		{
			name:  "line height above range",
			in:    Style{LineHeight: LineHeight{Value: 11}},
			paths: []string{"/lineHeight/value"},
			msg:   "line height 11 is out of range (0 < x <= 10)",
		},
		{
			name:  "line height NaN",
			in:    Style{LineHeight: LineHeight{Value: math.NaN()}},
			paths: []string{"/lineHeight/value"},
			msg:   "line height NaN is out of range",
		},
		{
			name:  "link color preset and value",
			in:    Style{LinkColor: Color{Preset: "a", Value: "#fff"}},
			paths: []string{"/linkColor"},
			msg:   "set either preset or value, not both",
		},
		{
			name: "every item invalid",
			in: Style{
				TextColor:       Color{Value: "red"},
				BackgroundColor: Color{Value: "transparent"},
				LinkColor:       Color{Preset: "link color"},
				Gradient:        Gradient{Preset: "a b"},
				FontSize:        Length{Value: "-1rem"},
				LetterSpacing:   Length{Preset: "wide"},
				FontFamily:      FontFamily{Preset: "Noto Sans"},
				FontWeight:      FontWeight{Value: 950},
				LineHeight:      LineHeight{Value: -1},
				Padding: Sides{
					Top:    Length{Value: "-1rem"},
					Right:  Length{Preset: "x y"},
					Bottom: Length{Value: "1"},
					Left:   Length{Value: "auto"},
				},
				Margin: Sides{
					Top:    Length{Value: "1"},
					Right:  Length{Preset: "x y"},
					Bottom: Length{Value: "1pt"},
					Left:   Length{Value: "calc(1px)"},
				},
				BlockGap: Length{Value: "-1rem"},
				Border: Border{
					Width:  Length{Value: "10%"},
					Style:  BorderStyle{Value: "double"},
					Color:  Color{Value: "#ggg"},
					Radius: Length{Preset: "round"},
				},
			},
			paths: []string{
				"/textColor/value", "/backgroundColor/value", "/linkColor/preset", "/gradient/preset",
				"/fontSize/value", "/letterSpacing/preset", "/fontFamily/preset", "/fontWeight/value", "/lineHeight/value",
				"/padding/top/value", "/padding/right/preset", "/padding/bottom/value", "/padding/left/value",
				"/margin/top/value", "/margin/right/preset", "/margin/bottom/value", "/margin/left/value",
				"/blockGap/value",
				"/border/width/value", "/border/style/value", "/border/color/value", "/border/radius/preset",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertStyleValidate(t, tt.in, tt.paths, tt.msg)
		})
	}
}

func TestStyleValues(t *testing.T) {
	every := fullStyle
	every.Margin.Right = Length{Value: "0"}
	every.Margin.Left = Length{Value: "5%"}
	padding := LengthRule{Preset: true, Percent: true}
	margin := LengthRule{Preset: true, Negative: true, Percent: true}
	tests := []struct {
		name  string
		in    Style
		want  []StyleValue
		paths []string
	}{
		{name: "zero", in: Style{}},
		{
			name: "every item, side and part",
			in:   every,
			want: []StyleValue{
				{Item: "textColor", Kind: "color", Preset: "primary", format: formatColor},
				{Item: "backgroundColor", Kind: "color", Value: "#fff4e5", format: formatColor},
				{Item: "linkColor", Kind: "color", Value: "rgb(30, 115, 190)", format: formatColor},
				{Item: "gradient", Kind: "gradient", Preset: "sunset"},
				{Item: "fontSize", Kind: "fontSize", Value: "1.25rem", format: formatLength, length: LengthRule{Preset: true, Percent: true}},
				{Item: "letterSpacing", Value: "0.05em", format: formatLength, length: LengthRule{Negative: true}},
				{Item: "fontFamily", Kind: "fontFamily", Preset: "serif"},
				{Item: "fontWeight", Value: "700", format: formatFontWeight},
				{Item: "lineHeight", Value: "1.5", format: formatLineHeight},
				{Item: "padding", Key: "top", Kind: "spacing", Value: "1rem", format: formatLength, length: padding},
				{Item: "padding", Key: "right", Kind: "spacing", Preset: "medium", format: formatLength, length: padding},
				{Item: "padding", Key: "bottom", Kind: "spacing", Value: "2rem", format: formatLength, length: padding},
				{Item: "padding", Key: "left", Kind: "spacing", Value: "5%", format: formatLength, length: padding},
				{Item: "margin", Key: "top", Kind: "spacing", Value: "-1rem", format: formatLength, length: margin},
				{Item: "margin", Key: "right", Kind: "spacing", Value: "0", format: formatLength, length: margin},
				{Item: "margin", Key: "bottom", Kind: "spacing", Preset: "large", format: formatLength, length: margin},
				{Item: "margin", Key: "left", Kind: "spacing", Value: "5%", format: formatLength, length: margin},
				{Item: "blockGap", Kind: "spacing", Preset: "small", format: formatLength, length: LengthRule{Preset: true, Percent: true}},
				{Item: "border", Key: "width", Value: "1px", format: formatLength},
				{Item: "border", Key: "style", Value: "solid", format: formatBorderStyle},
				{Item: "border", Key: "color", Kind: "color", Preset: "accent", format: formatColor},
				{Item: "border", Key: "radius", Value: "4px", format: formatLength, length: LengthRule{Percent: true}},
			},
			paths: []string{
				"/textColor", "/backgroundColor", "/linkColor", "/gradient", "/fontSize", "/letterSpacing", "/fontFamily",
				"/fontWeight", "/lineHeight", "/padding/top", "/padding/right", "/padding/bottom", "/padding/left",
				"/margin/top", "/margin/right", "/margin/bottom", "/margin/left", "/blockGap",
				"/border/width", "/border/style", "/border/color", "/border/radius",
			},
		},
		{
			name: "only the sides and parts that are set",
			in: Style{
				Padding: Sides{Left: Length{Preset: "small"}},
				Margin:  Sides{Bottom: Length{Value: "auto"}},
				Border:  Border{Color: Color{Value: "#000"}},
			},
			want: []StyleValue{
				{Item: "padding", Key: "left", Kind: "spacing", Preset: "small", format: formatLength, length: padding},
				{Item: "margin", Key: "bottom", Kind: "spacing", Value: "auto", format: formatLength, length: margin},
				{Item: "border", Key: "color", Kind: "color", Value: "#000", format: formatColor},
			},
			paths: []string{"/padding/left", "/margin/bottom", "/border/color"},
		},
		{
			name: "values as written, without checks",
			in: Style{
				TextColor:     Color{Preset: "a b", Value: "red"},
				LetterSpacing: Length{Preset: "wide"},
				Border:        Border{Style: BorderStyle{Value: "double"}},
			},
			want: []StyleValue{
				{Item: "textColor", Kind: "color", Preset: "a b", Value: "red", format: formatColor},
				{Item: "letterSpacing", Preset: "wide", format: formatLength, length: LengthRule{Negative: true}},
				{Item: "border", Key: "style", Value: "double", format: formatBorderStyle},
			},
			paths: []string{"/textColor", "/letterSpacing", "/border/style"},
		},
		{
			name: "numbers",
			in:   Style{FontWeight: FontWeight{Value: 100}, LineHeight: LineHeight{Value: 1e-7}},
			want: []StyleValue{
				{Item: "fontWeight", Value: "100", format: formatFontWeight},
				{Item: "lineHeight", Value: "0.0000001", format: formatLineHeight},
			},
			paths: []string{"/fontWeight", "/lineHeight"},
		},
		{
			name: "numbers out of range",
			in:   Style{FontWeight: FontWeight{Value: -5}, LineHeight: LineHeight{Value: 1e21}},
			want: []StyleValue{
				{Item: "fontWeight", Value: "-5", format: formatFontWeight},
				{Item: "lineHeight", Value: "1000000000000000000000", format: formatLineHeight},
			},
			paths: []string{"/fontWeight", "/lineHeight"},
		},
		{
			name:  "NaN line height",
			in:    Style{LineHeight: LineHeight{Value: math.NaN()}},
			want:  []StyleValue{{Item: "lineHeight", Value: "NaN", format: formatLineHeight}},
			paths: []string{"/lineHeight"},
		},
		{name: "negative zero line height as unset", in: Style{LineHeight: LineHeight{Value: math.Copysign(0, -1)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.Values()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Values() = %#v, want %#v", got, tt.want)
			}
			var paths []string
			for _, v := range got {
				paths = append(paths, v.Path())
			}
			if !reflect.DeepEqual(paths, tt.paths) {
				t.Errorf("Path() of Values() = %q, want %q", paths, tt.paths)
			}
		})
	}
}

// TestStyleItem checks item against the fields of Style, with fullStyle, whose items of the same type differ.
func TestStyleItem(t *testing.T) {
	v := reflect.ValueOf(fullStyle)
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		if got, want := fullStyle.item(name), v.Field(i).Interface(); got != want {
			t.Errorf("item(%q) = %#v, want %#v", name, got, want)
		}
	}
	if got := fullStyle.item("shadow"); got != nil {
		t.Errorf(`item("shadow") = %#v, want nil`, got)
	}
}

func TestStyleValueCheck(t *testing.T) {
	const invalidName = `: use ASCII letters, digits, "-" and "_"`
	tests := []struct {
		name string
		in   Style  // with one value
		want string // the text of the error, "" for none
	}{
		{name: "color preset", in: Style{TextColor: Color{Preset: "primary"}}},
		{name: "color value", in: Style{BackgroundColor: Color{Value: "rgb(30 115 190 / 50%)"}}},
		{name: "invalid color", in: Style{LinkColor: Color{Value: "red"}}, want: `/value: invalid color "red"`},
		{name: "invalid color preset", in: Style{TextColor: Color{Preset: "a b"}}, want: `/preset: invalid name "a b"` + invalidName},
		{
			name: "color preset and value",
			in:   Style{TextColor: Color{Preset: "primary", Value: "#fff"}},
			want: "set either preset or value, not both",
		},
		{
			name: "first of several problems",
			in:   Style{FontSize: Length{Preset: "a b", Value: "1"}},
			want: "set either preset or value, not both",
		},
		{name: "gradient", in: Style{Gradient: Gradient{Preset: "sunset"}}},
		{name: "invalid gradient", in: Style{Gradient: Gradient{Preset: "a/b"}}, want: `/preset: invalid name "a/b"` + invalidName},
		{name: "font size preset", in: Style{FontSize: Length{Preset: "large"}}},
		{name: "font size percentage", in: Style{FontSize: Length{Value: "120%"}}},
		{
			name: "negative font size",
			in:   Style{FontSize: Length{Value: "-1rem"}},
			want: `/value: negative length "-1rem" is not allowed here`,
		},
		{name: "negative letter spacing", in: Style{LetterSpacing: Length{Value: "-0.05em"}}},
		{name: "letter spacing preset", in: Style{LetterSpacing: Length{Preset: "wide"}}, want: "/preset: presets are not allowed here"},
		{
			name: "letter spacing percentage",
			in:   Style{LetterSpacing: Length{Value: "5%"}},
			want: `/value: percentage "5%" is not allowed here`,
		},
		{name: "font family", in: Style{FontFamily: FontFamily{Preset: "serif"}}},
		{
			name: "invalid font family",
			in:   Style{FontFamily: FontFamily{Preset: "Noto Sans"}},
			want: `/preset: invalid name "Noto Sans"` + invalidName,
		},
		{name: "font weight", in: Style{FontWeight: FontWeight{Value: 100}}},
		{
			name: "heavy font weight",
			in:   Style{FontWeight: FontWeight{Value: 950}},
			want: "/value: font weight 950 is out of range (100-900)",
		},
		{
			name: "negative font weight",
			in:   Style{FontWeight: FontWeight{Value: -5}},
			want: "/value: font weight -5 is out of range (100-900)",
		},
		{name: "line height", in: Style{LineHeight: LineHeight{Value: 10}}},
		{name: "small line height", in: Style{LineHeight: LineHeight{Value: 1e-7}}},
		{
			name: "large line height",
			in:   Style{LineHeight: LineHeight{Value: 10.5}},
			want: "/value: line height 10.5 is out of range (0 < x <= 10)",
		},
		{
			name: "huge line height",
			in:   Style{LineHeight: LineHeight{Value: 1e21}},
			want: "/value: line height 1e+21 is out of range (0 < x <= 10)",
		},
		{
			name: "negative line height",
			in:   Style{LineHeight: LineHeight{Value: -1}},
			want: "/value: line height -1 is out of range (0 < x <= 10)",
		},
		{
			name: "NaN line height",
			in:   Style{LineHeight: LineHeight{Value: math.NaN()}},
			want: "/value: line height NaN is out of range (0 < x <= 10)",
		},
		{
			name: "infinite line height",
			in:   Style{LineHeight: LineHeight{Value: math.Inf(1)}},
			want: "/value: line height +Inf is out of range (0 < x <= 10)",
		},
		{name: "padding", in: Style{Padding: Sides{Top: Length{Value: "1rem"}}}},
		{name: "padding preset", in: Style{Padding: Sides{Right: Length{Preset: "medium"}}}},
		{
			name: "negative padding",
			in:   Style{Padding: Sides{Bottom: Length{Value: "-1rem"}}},
			want: `/value: negative length "-1rem" is not allowed here`,
		},
		{name: "unitless padding", in: Style{Padding: Sides{Left: Length{Value: "1"}}}, want: `/value: invalid length "1"`},
		{name: "negative margin", in: Style{Margin: Sides{Left: Length{Value: "-1rem"}}}},
		{name: "margin percentage", in: Style{Margin: Sides{Top: Length{Value: "5%"}}}},
		{name: "margin preset", in: Style{Margin: Sides{Bottom: Length{Preset: "large"}}}},
		{name: "margin in points", in: Style{Margin: Sides{Right: Length{Value: "1pt"}}}, want: `/value: invalid length "1pt"`},
		{name: "block gap preset", in: Style{BlockGap: Length{Preset: "small"}}},
		{name: "block gap percentage", in: Style{BlockGap: Length{Value: "10%"}}},
		{
			name: "negative block gap",
			in:   Style{BlockGap: Length{Value: "-1rem"}},
			want: `/value: negative length "-1rem" is not allowed here`,
		},
		{name: "border width", in: Style{Border: Border{Width: Length{Value: "1px"}}}},
		{
			name: "border width percentage",
			in:   Style{Border: Border{Width: Length{Value: "10%"}}},
			want: `/value: percentage "10%" is not allowed here`,
		},
		{
			name: "border width preset",
			in:   Style{Border: Border{Width: Length{Preset: "thin"}}},
			want: "/preset: presets are not allowed here",
		},
		{name: "border style", in: Style{Border: Border{Style: BorderStyle{Value: "dashed"}}}},
		{
			name: "unknown border style",
			in:   Style{Border: Border{Style: BorderStyle{Value: "double"}}},
			want: `/value: unknown border style "double"`,
		},
		{
			name: "border style in upper case",
			in:   Style{Border: Border{Style: BorderStyle{Value: "SOLID"}}},
			want: `/value: unknown border style "SOLID"`,
		},
		{name: "border color", in: Style{Border: Border{Color: Color{Preset: "accent"}}}},
		{
			name: "invalid border color",
			in:   Style{Border: Border{Color: Color{Value: "#ggg"}}},
			want: `/value: invalid color "#ggg"`,
		},
		{name: "border radius", in: Style{Border: Border{Radius: Length{Value: "50%"}}}},
		{
			name: "border radius preset",
			in:   Style{Border: Border{Radius: Length{Preset: "round"}}},
			want: "/preset: presets are not allowed here",
		},
		{
			name: "negative border radius",
			in:   Style{Border: Border{Radius: Length{Value: "-4px"}}},
			want: `/value: negative length "-4px" is not allowed here`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vs := tt.in.Values()
			if len(vs) != 1 {
				t.Fatalf("Values() = %#v, want one value", vs)
			}
			v := vs[0]
			err := v.Check()
			if got := errorMsg(err); got != tt.want {
				t.Errorf("Check() = %q, want %q", got, tt.want)
			}
			// Check returns the first problem that Validate finds, relative to the path of the value.
			verr := tt.in.Validate()
			if err == nil {
				if verr != nil {
					t.Errorf("Validate() = %v, want nil", verr)
				}
				return
			}
			var e *Error
			var es Errors
			if !errors.As(err, &e) || !errors.As(verr, &es) {
				t.Fatalf("Check() = %v (%T) and Validate() = %v (%T), want *Error and Errors", err, err, verr, verr)
			}
			if es[0].Path != v.Path()+e.Path || es[0].Msg != e.Msg {
				t.Errorf("first problem of Validate() = %q at %q, want %q at %q", es[0].Msg, es[0].Path, e.Msg, v.Path()+e.Path)
			}
		})
	}
}

// TestStyleValueCheckGoValues checks values built in Go, some of which Values does not make.
func TestStyleValueCheckGoValues(t *testing.T) {
	// changed is the value of s, which has one, with value instead of its own.
	changed := func(s Style, value string) StyleValue {
		v := s.Values()[0]
		v.Value = value
		return v
	}
	tests := []struct {
		name string
		in   StyleValue
		want string // the text of the error, "" for none
	}{
		{name: "zero", in: StyleValue{}},
		{name: "preset without a format", in: StyleValue{Item: "textColor", Kind: "color", Preset: "primary"}},
		{
			name: "value without a format",
			in:   StyleValue{Item: "textColor", Kind: "color", Value: "#fff"},
			want: "/value: values are not allowed here",
		},
		{
			name: "gradient value",
			in:   StyleValue{Item: "gradient", Kind: "gradient", Value: "linear-gradient(#fff, #000)"},
			want: "/value: values are not allowed here",
		},
		{
			name: "font weight preset",
			in:   StyleValue{Item: "fontWeight", Preset: "bold", format: formatFontWeight},
			want: "/preset: presets are not allowed here",
		},
		{
			name: "font weight that is not a number",
			in:   StyleValue{Item: "fontWeight", Value: "bold", format: formatFontWeight},
			want: "/value: must be an integer",
		},
		{
			name: "line height that is not a number",
			in:   StyleValue{Item: "lineHeight", Value: "normal", format: formatLineHeight},
			want: "/value: invalid number",
		},
		{
			name: "line height overflow",
			in:   StyleValue{Item: "lineHeight", Value: "1e400", format: formatLineHeight},
			want: "/value: invalid number",
		},
		{name: "changed color", in: changed(Style{TextColor: Color{Value: "#fff"}}, "red"), want: `/value: invalid color "red"`},
		{
			name: "changed length",
			in:   changed(Style{Padding: Sides{Top: Length{Value: "1rem"}}}, "-1rem"),
			want: `/value: negative length "-1rem" is not allowed here`,
		},
		{
			name: "changed font weight",
			in:   changed(Style{FontWeight: FontWeight{Value: 700}}, "950"),
			want: "/value: font weight 950 is out of range (100-900)",
		},
		{
			name: "changed line height",
			in:   changed(Style{LineHeight: LineHeight{Value: 1.5}}, "12"),
			want: "/value: line height 12 is out of range (0 < x <= 10)",
		},
		{
			name: "changed border style",
			in:   changed(Style{Border: Border{Style: BorderStyle{Value: "solid"}}}, "none"),
			want: `/value: unknown border style "none"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Check()
			if got := errorMsg(err); got != tt.want {
				t.Errorf("Check() = %q, want %q", got, tt.want)
			}
			var e *Error
			if err != nil && !errors.As(err, &e) {
				t.Errorf("Check() = %v (%T), want *Error", err, err)
			}
		})
	}
}

func TestHighlightValues(t *testing.T) {
	tests := []struct {
		name   string
		in     Highlight
		want   []StyleValue
		paths  []string
		checks []string // the texts of the errors of Check of the values, "" for none
	}{
		{name: "variant", in: Highlight{Variant: "marker"}},
		{
			name: "both colors",
			in:   Highlight{Color: Color{Preset: "accent"}, BackgroundColor: Color{Value: "#fff4e5"}},
			want: []StyleValue{
				{Item: "color", Kind: "color", Preset: "accent", format: formatColor},
				{Item: "backgroundColor", Kind: "color", Value: "#fff4e5", format: formatColor},
			},
			paths:  []string{"/color", "/backgroundColor"},
			checks: []string{"", ""},
		},
		{
			name:   "background color",
			in:     Highlight{BackgroundColor: Color{Preset: "muted"}},
			want:   []StyleValue{{Item: "backgroundColor", Kind: "color", Preset: "muted", format: formatColor}},
			paths:  []string{"/backgroundColor"},
			checks: []string{""},
		},
		{
			name: "invalid colors with a variant",
			in:   Highlight{Variant: "marker", Color: Color{Value: "red"}, BackgroundColor: Color{Preset: "a", Value: "#fff"}},
			want: []StyleValue{
				{Item: "color", Kind: "color", Value: "red", format: formatColor},
				{Item: "backgroundColor", Kind: "color", Preset: "a", Value: "#fff", format: formatColor},
			},
			paths:  []string{"/color", "/backgroundColor"},
			checks: []string{`/value: invalid color "red"`, "set either preset or value, not both"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.Values()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Values() = %#v, want %#v", got, tt.want)
			}
			var paths, checks []string
			for _, v := range got {
				paths = append(paths, v.Path())
				checks = append(checks, errorMsg(v.Check()))
			}
			if !reflect.DeepEqual(paths, tt.paths) {
				t.Errorf("Path() of Values() = %q, want %q", paths, tt.paths)
			}
			if !reflect.DeepEqual(checks, tt.checks) {
				t.Errorf("Check() of Values() = %q, want %q", checks, tt.checks)
			}
		})
	}
}

func TestAttrsHeightValue(t *testing.T) {
	// height is the value of the height of a spacer with preset and value.
	height := func(preset, value string) StyleValue {
		return StyleValue{
			Item: "height", Kind: "spacing", Preset: preset, Value: value, format: formatLength, length: LengthRule{Preset: true},
		}
	}
	tests := []struct {
		name  string
		in    Attrs
		want  StyleValue
		check string // the text of the error of Check, "" for none
	}{
		{name: "unset", in: Attrs{Label: "a", Width: "50%"}},
		{name: "preset", in: Attrs{Height: Length{Preset: "large"}}, want: height("large", "")},
		{name: "value", in: Attrs{Height: Length{Value: "40px"}}, want: height("", "40px")},
		{name: "zero", in: Attrs{Height: Length{Value: "0"}}, want: height("", "0")},
		{
			name:  "percentage",
			in:    Attrs{Height: Length{Value: "50%"}},
			want:  height("", "50%"),
			check: `/value: percentage "50%" is not allowed here`,
		},
		{
			name:  "negative",
			in:    Attrs{Height: Length{Value: "-1px"}},
			want:  height("", "-1px"),
			check: `/value: negative length "-1px" is not allowed here`,
		},
		{
			name:  "invalid preset",
			in:    Attrs{Height: Length{Preset: "a b"}},
			want:  height("a b", ""),
			check: `/preset: invalid name "a b": use ASCII letters, digits, "-" and "_"`,
		},
		{
			name:  "preset and value",
			in:    Attrs{Height: Length{Preset: "large", Value: "40px"}},
			want:  height("large", "40px"),
			check: "set either preset or value, not both",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.HeightValue()
			if got != tt.want {
				t.Fatalf("HeightValue() = %#v, want %#v", got, tt.want)
			}
			if got != (StyleValue{}) && got.Path() != "/height" {
				t.Errorf("Path() = %q, want %q", got.Path(), "/height")
			}
			if c := errorMsg(got.Check()); c != tt.check {
				t.Errorf("Check() = %q, want %q", c, tt.check)
			}
		})
	}
}
