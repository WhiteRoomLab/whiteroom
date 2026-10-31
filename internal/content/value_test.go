package content

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestParseNumber(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want float64
		ok   bool
	}{
		{name: "zero", in: "0", want: 0, ok: true},
		{name: "integer", in: "255", want: 255, ok: true},
		{name: "fraction", in: "1.5", want: 1.5, ok: true},
		{name: "leading dot", in: ".5", want: 0.5, ok: true},
		{name: "leading zeros", in: "007", want: 7, ok: true},

		{name: "empty", in: ""},
		{name: "dot only", in: "."},
		{name: "trailing dot", in: "1."},
		{name: "two dots", in: "1.2.3"},
		{name: "plus sign", in: "+1"},
		{name: "minus sign", in: "-1"},
		{name: "exponent", in: "1e2"},
		{name: "hex float", in: "0x1p-2"},
		{name: "underscore", in: "1_000"},
		{name: "infinity", in: "Inf"},
		{name: "NaN", in: "NaN"},
		{name: "leading space", in: " 1"},
		{name: "trailing newline", in: "1\n"},
		{name: "too large", in: strings.Repeat("9", 400)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseNumber(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseNumber(%q) = %v, %t, want %v, %t", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCheckColor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{name: "hex 3", in: "#fff", ok: true},
		{name: "hex 3 upper case", in: "#FFF", ok: true},
		{name: "hex 4", in: "#ffff", ok: true},
		{name: "hex 6", in: "#1e73be", ok: true},
		{name: "hex 8", in: "#1e73be80", ok: true},
		{name: "hex mixed case", in: "#AbCdEf", ok: true},
		{name: "rgb with commas", in: "rgb(30, 115, 190)", ok: true},
		{name: "rgba with commas", in: "rgba(0,0,0,.5)", ok: true},
		{name: "rgb with spaces", in: "rgb(30 115 190)", ok: true},
		{name: "rgb with percentage alpha", in: "rgb(30 115 190 / 50%)", ok: true},
		{name: "rgb mixing percentage and numbers", in: "rgb(100% 0 0)", ok: true},
		{name: "hsl with commas", in: "hsl(210, 73%, 43%)", ok: true},
		{name: "hsl with deg and alpha", in: "hsl(210deg 73% 43% / 0.5)", ok: true},
		{name: "spaces around commas", in: "rgb( 0 , 0 , 0 )", ok: true},
		{name: "rgba without alpha", in: "rgba(0 0 0)", ok: true},
		{name: "rgb with alpha after comma", in: "rgb(0,0,0,0.5)", ok: true},
		{name: "percentage alpha after numbers", in: "rgba(0, 0, 0, 50%)", ok: true},
		{name: "hsla without alpha", in: "hsla(120 100% 50%)", ok: true},
		{name: "slash without spaces", in: "rgb(0 0 0/.5)", ok: true},
		{name: "hsl maximum hue", in: "hsl(360 0% 100%)", ok: true},
		{name: "rgb maximums", in: "rgb(255 255 255 / 1)", ok: true},
		{name: "fractional channel", in: "rgb(127.5 0 0)", ok: true},
		{name: "deg with commas", in: "hsl(210deg, 73%, 43%)", ok: true},
		{name: "two spaces", in: "rgb(0  0  0)", ok: true},

		{name: "empty", in: ""},
		{name: "hex 2", in: "#ff"},
		{name: "hex 5", in: "#fffff"},
		{name: "not hex", in: "#ggg"},
		{name: "hex without number sign", in: "fff"},
		{name: "two number signs", in: "##fff"},
		{name: "named color", in: "red"},
		{name: "upper-case function", in: "RGB(0,0,0)"},
		{name: "channel over 255", in: "rgb(256,0,0)"},
		{name: "negative channel", in: "rgb(-1,0,0)"},
		{name: "exponent", in: "rgb(1e2,0,0)"},
		{name: "mixed kinds with commas", in: "rgb(100%,0,0)"},
		{name: "slash with commas", in: "rgb(0,0,0 / 0.5)"},
		{name: "alpha without slash", in: "rgb(0 0 0 0)"},
		{name: "trailing comma", in: "rgb(0,0,0,)"},
		{name: "five arguments", in: "rgb(0,0,0,0,0)"},
		{name: "two channels", in: "rgb(0 0)"},
		{name: "no arguments", in: "rgb()"},
		{name: "tabs", in: "rgb(0\t0\t0)"},
		{name: "tab after comma", in: "rgb(0,\t0,0)"},
		{name: "tab after slash", in: "rgb(0 0 0 /\t0.5)"},
		{name: "alpha over 1", in: "rgb(0 0 0 / 1.5)"},
		{name: "alpha over 100%", in: "rgb(0 0 0 / 150%)"},
		{name: "hue over 360", in: "hsl(361,0%,0%)"},
		{name: "turn", in: "hsl(0.5turn 50% 50%)"},
		{name: "saturation and lightness without percent", in: "hsl(0, 50, 50)"},
		{name: "saturation without percent", in: "hsl(0 50 50%)"},
		{name: "lightness without percent", in: "hsl(0 50% 50)"},
		{name: "calc", in: "calc(1)"},
		{name: "var", in: "var(--x)"},
		{name: "url", in: "url(x)"},
		{name: "semicolon after hex", in: "#fff;"},
		{name: "brace after hex", in: "#fff }"},
		{name: "space before parenthesis", in: "rgb (0,0,0)"},
		{name: "trailing space", in: "rgb(0,0,0) "},
		{name: "no closing parenthesis", in: "rgb(0,0,0"},
		{name: "leading space", in: " #fff"},
		{name: "commas and spaces", in: "rgb(0, 0 0)"},
		{name: "mixed kinds with commas and number last", in: "rgb(50%, 0%, 0)"},
		{name: "two slashes", in: "rgb(0 0 0 / 0.5 / 1)"},
		{name: "slash without alpha", in: "rgb(0 0 0 /)"},
		{name: "plus sign", in: "rgb(+1,0,0)"},
		{name: "trailing dot", in: "rgb(5.,0,0)"},
		{name: "negative hue", in: "hsl(-1 50% 50%)"},
		{name: "upper-case deg", in: "hsl(210DEG 50% 50%)"},
		{name: "transparent", in: "transparent"},
		{name: "currentcolor", in: "currentcolor"},
		{name: "hwb", in: "hwb(0 0% 0%)"},
		{name: "oklch", in: "oklch(0.7 0.1 200)"},
		{name: "color function", in: "color(srgb 1 0 0)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = fmt.Sprintf("invalid color %q", tt.in)
			}
			assertErrorMsg(t, CheckColor(tt.in), msg)
		})
	}
}

func TestCheckLength(t *testing.T) {
	const (
		invalid    = "invalid length %q"
		negative   = "negative length %q is not allowed here"
		percentage = "percentage %q is not allowed here"
	)
	lenient := LengthRule{Negative: true, Percent: true}
	tests := []struct {
		name string
		in   string
		rule LengthRule
		msg  string // format of the message for in, or "" for no error
	}{
		{name: "zero", in: "0"},
		{name: "zero px", in: "0px"},
		{name: "rem", in: "1rem"},
		{name: "fractional em", in: "1.5em"},
		{name: "leading dot", in: ".5rem"},
		{name: "vw", in: "100vw"},
		{name: "vh", in: "50vh"},

		{name: "preset allowed", in: "1rem", rule: LengthRule{Preset: true}},
		{name: "percentage allowed", in: "50%", rule: LengthRule{Percent: true}},
		{name: "percentage not allowed", in: "50%", msg: percentage},
		{name: "percentage with preset", in: "50%", rule: LengthRule{Preset: true}, msg: percentage},
		{name: "negative allowed", in: "-1px", rule: LengthRule{Negative: true}},
		{name: "negative not allowed", in: "-1px", rule: LengthRule{Percent: true}, msg: negative},
		{name: "negative with preset", in: "-1px", rule: LengthRule{Preset: true}, msg: negative},
		{name: "invalid with preset", in: "auto", rule: LengthRule{Preset: true}, msg: invalid},
		{name: "negative percentage allowed", in: "-50%", rule: lenient},
		{name: "negative percentage without negative", in: "-50%", rule: LengthRule{Percent: true}, msg: negative},
		{name: "negative percentage without percent", in: "-50%", rule: LengthRule{Negative: true}, msg: percentage},
		{name: "negative before percentage", in: "-50%", msg: negative},
		{name: "invalid before negative", in: "-1.px", msg: invalid},
		{name: "invalid before percentage", in: "1.%", msg: invalid},

		{name: "empty", in: "", rule: lenient, msg: invalid},
		{name: "unitless", in: "1", rule: lenient, msg: invalid},
		{name: "unitless zero with fraction", in: "0.0", rule: lenient, msg: invalid},
		{name: "unitless double zero", in: "00", rule: lenient, msg: invalid},
		{name: "plus sign", in: "+1px", rule: lenient, msg: invalid},
		{name: "two minus signs", in: "--1px", rule: lenient, msg: invalid},
		{name: "upper-case unit", in: "1PX", rule: lenient, msg: invalid},
		{name: "space before unit", in: "1 px", rule: lenient, msg: invalid},
		{name: "leading space", in: " 1px", rule: lenient, msg: invalid},
		{name: "trailing space", in: "1px ", rule: lenient, msg: invalid},
		{name: "exponent", in: "1e2px", rule: lenient, msg: invalid},
		{name: "trailing dot", in: "1.px", rule: lenient, msg: invalid},
		{name: "negative unitless zero", in: "-0", rule: lenient, msg: invalid},
		{name: "pt", in: "1pt", rule: lenient, msg: invalid},
		{name: "ch", in: "1ch", rule: lenient, msg: invalid},
		{name: "auto", in: "auto", rule: lenient, msg: invalid},
		{name: "calc", in: "calc(1px + 2px)", rule: lenient, msg: invalid},
		{name: "var", in: "var(--x)", rule: lenient, msg: invalid},
		{name: "semicolon", in: "1px;", rule: lenient, msg: invalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ""
			if tt.msg != "" {
				msg = fmt.Sprintf(tt.msg, tt.in)
			}
			assertErrorMsg(t, CheckLength(tt.in, tt.rule), msg)
		})
	}
}

// mobileWidthHint follows the value in the message of CheckMobileWidth.
const mobileWidthHint = ": use a length greater than 0 in px, em or rem, such as 781px"

func TestCheckMobileWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{name: "px", in: "781px", ok: true},
		{name: "rem", in: "48rem", ok: true},
		{name: "fractional em", in: "37.5em", ok: true},
		{name: "leading dot", in: ".5rem", ok: true},
		{name: "small", in: "0.01px", ok: true},
		{name: "leading zero", in: "0781px", ok: true},

		{name: "empty", in: ""},
		{name: "unitless zero", in: "0"},
		{name: "zero px", in: "0px"},
		{name: "zero with a fraction", in: "0.0em"},
		{name: "negative", in: "-1px"},
		{name: "plus sign", in: "+1px"},
		{name: "percentage", in: "10%"},
		{name: "vw", in: "10vw"},
		{name: "vh", in: "10vh"},
		{name: "pt", in: "10pt"},
		{name: "unitless", in: "781"},
		{name: "unit only", in: "px"},
		{name: "space before unit", in: "781 px"},
		{name: "leading space", in: " 781px"},
		{name: "trailing space", in: "781px "},
		{name: "exponent", in: "1e2px"},
		{name: "trailing dot", in: "1.px"},
		{name: "two dots", in: "1.2.3px"},
		{name: "upper-case unit", in: "781PX"},
		{name: "two units", in: "1pxpx"},
		{name: "calc", in: "calc(781px)"},
		{name: "var", in: "var(--x)"},
		{name: "end of a media query", in: "781px) and (min-width: 0px"},
		{name: "semicolon", in: "781px;"},
		{name: "too large", in: strings.Repeat("9", 400) + "px"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = fmt.Sprintf("invalid mobileWidth %q", tt.in) + mobileWidthHint
			}
			assertErrorMsg(t, CheckMobileWidth(tt.in), msg)
		})
	}
}

func TestCheckFontWeight(t *testing.T) {
	tests := []struct {
		name string
		in   int
		ok   bool
	}{
		{name: "minimum", in: 100, ok: true},
		{name: "normal", in: 400, ok: true},
		{name: "maximum", in: 900, ok: true},
		{name: "zero", in: 0},
		{name: "below minimum", in: 99},
		{name: "above maximum", in: 901},
		{name: "negative", in: -100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = fmt.Sprintf("font weight %d is out of range (100-900)", tt.in)
			}
			assertErrorMsg(t, checkFontWeight(tt.in), msg)
		})
	}
}

func TestCheckLineHeight(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		ok   bool
	}{
		{name: "small", in: 0.1, ok: true},
		{name: "typical", in: 1.5, ok: true},
		{name: "maximum", in: 10, ok: true},
		{name: "zero", in: 0},
		{name: "negative zero", in: math.Copysign(0, -1)},
		{name: "negative", in: -1},
		{name: "above maximum", in: 10.01},
		{name: "NaN", in: math.NaN()},
		{name: "infinity", in: math.Inf(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = fmt.Sprintf("line height %v is out of range (0 < x <= 10)", tt.in)
			}
			assertErrorMsg(t, checkLineHeight(tt.in), msg)
		})
	}
}
