package content

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"reflect"
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
