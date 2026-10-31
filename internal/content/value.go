package content

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	numberPattern   = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)
	hexColorPattern = regexp.MustCompile(`^#(?:[0-9A-Fa-f]{3,4}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$`)
)

func parseNumber(s string) (float64, bool) {
	// ParseFloat alone would also accept "+1", "1e2", "1.", "Inf" and "0x1p-2".
	if !numberPattern.MatchString(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// inRange reports whether s is a number from 0 to limit.
func inRange(s string, limit float64) bool {
	v, ok := parseNumber(s)
	return ok && v <= limit
}

func isPercentage(s string) bool {
	num, ok := strings.CutSuffix(s, "%")
	return ok && inRange(num, 100)
}

// CheckColor checks s by the rule for colors written directly: hex colors and the rgb(), rgba(), hsl() and hsla()
// functions.
func CheckColor(s string) error {
	return checkColor(s)
}

func checkColor(s string) error {
	if !isColor(s) {
		return fmt.Errorf("invalid color %q", s)
	}
	return nil
}

func isColor(s string) bool {
	if strings.HasPrefix(s, "#") {
		return hexColorPattern.MatchString(s)
	}
	name, rest, ok := strings.Cut(s, "(")
	if !ok {
		return false
	}
	inner, ok := strings.CutSuffix(rest, ")")
	if !ok {
		return false
	}
	args, comma := colorArgs(inner)
	if args == nil {
		return false
	}
	// As in CSS Color 4, rgba and hsla take the same arguments as rgb and hsl.
	switch name {
	case "rgb", "rgba":
		for _, a := range args[:3] {
			if !inRange(a, 255) && !isPercentage(a) {
				return false
			}
			// The comma syntax does not mix numbers and percentages.
			if comma && isPercentage(a) != isPercentage(args[0]) {
				return false
			}
		}
	case "hsl", "hsla":
		hue := strings.TrimSuffix(args[0], "deg")
		if !inRange(hue, 360) || !isPercentage(args[1]) || !isPercentage(args[2]) {
			return false
		}
	default:
		return false
	}
	return len(args) == 3 || inRange(args[3], 1) || isPercentage(args[3])
}

// colorArgs splits the arguments of rgb() or hsl() into three channels and an optional alpha.
// It returns nil for a wrong number of arguments.
func colorArgs(s string) (args []string, comma bool) {
	// Only U+0020 separates arguments, not tabs or other spaces.
	isSpace := func(r rune) bool { return r == ' ' }
	if strings.Contains(s, ",") {
		args = strings.Split(s, ",")
		if len(args) != 3 && len(args) != 4 {
			return nil, false
		}
		for i := range args {
			args[i] = strings.TrimFunc(args[i], isSpace)
		}
		return args, true
	}
	channels, alpha, slash := strings.Cut(s, "/")
	args = strings.FieldsFunc(channels, isSpace)
	if len(args) != 3 {
		return nil, false
	}
	if !slash {
		return args, false
	}
	alphas := strings.FieldsFunc(alpha, isSpace)
	if len(alphas) != 1 {
		return nil, false
	}
	return append(args, alphas[0]), false
}

type LengthRule struct {
	Preset   bool // theme presets are allowed; CheckLength ignores it
	Negative bool // negative lengths are allowed
	Percent  bool // percentages are allowed
}

var lengthUnits = []string{"px", "rem", "em", "%", "vw", "vh"}

// CheckLength checks s by the rule for lengths written directly: "0", or a number and one of the units px, rem, em, %,
// vw and vh, such as "1.5rem". r tells whether negative lengths and percentages are allowed.
func CheckLength(s string, r LengthRule) error {
	return checkLength(s, r)
}

func checkLength(s string, r LengthRule) error {
	// "0" is the only unitless length; "0.0", "00" and "-0" are invalid.
	if s == "0" {
		return nil
	}
	body, negative := strings.CutPrefix(s, "-")
	unit := strings.TrimLeft(body, "0123456789.")
	if _, ok := parseNumber(strings.TrimSuffix(body, unit)); !ok || !slices.Contains(lengthUnits, unit) {
		return fmt.Errorf("invalid length %q", s)
	}
	if negative && !r.Negative {
		return fmt.Errorf("negative length %q is not allowed here", s)
	}
	if unit == "%" && !r.Percent {
		return fmt.Errorf("percentage %q is not allowed here", s)
	}
	return nil
}

// CheckMobileWidth checks s by the rule for the mobileWidth of a theme: a length greater than 0 in px, em or rem.
func CheckMobileWidth(s string) error {
	unit := strings.TrimLeft(s, "0123456789.")
	v, ok := parseNumber(strings.TrimSuffix(s, unit))
	if !ok || v <= 0 || !slices.Contains([]string{"px", "em", "rem"}, unit) {
		return fmt.Errorf("invalid mobileWidth %q: use a length greater than 0 in px, em or rem, such as 781px", s)
	}
	return nil
}

func checkFontWeight(v int) error {
	if v < 100 || v > 900 {
		return fmt.Errorf("font weight %d is out of range (100-900)", v)
	}
	return nil
}

func checkLineHeight(v float64) error {
	// Inverting this test would let NaN through.
	if v > 0 && v <= 10 {
		return nil
	}
	return fmt.Errorf("line height %v is out of range (0 < x <= 10)", v)
}
