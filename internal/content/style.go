package content

import (
	"errors"
	"fmt"
	"strconv"
)

type Color struct {
	Preset string `json:"preset,omitempty"`
	Value  string `json:"value,omitempty"`
}

type Length struct {
	Preset string `json:"preset,omitempty"`
	Value  string `json:"value,omitempty"`
}

type Gradient struct {
	Preset string `json:"preset,omitempty"`
}

type FontFamily struct {
	Preset string `json:"preset,omitempty"`
}

type FontWeight struct {
	Value int `json:"value,omitzero"`
}

type LineHeight struct {
	Value float64 `json:"value,omitzero"`
}

type BorderStyle struct {
	Value string `json:"value,omitempty"`
}

type Sides struct {
	Top    Length `json:"top,omitzero"`
	Right  Length `json:"right,omitzero"`
	Bottom Length `json:"bottom,omitzero"`
	Left   Length `json:"left,omitzero"`
}

type Border struct {
	Width  Length      `json:"width,omitzero"`
	Style  BorderStyle `json:"style,omitzero"`
	Color  Color       `json:"color,omitzero"`
	Radius Length      `json:"radius,omitzero"`
}

type Style struct {
	TextColor       Color      `json:"textColor,omitzero"`
	BackgroundColor Color      `json:"backgroundColor,omitzero"`
	LinkColor       Color      `json:"linkColor,omitzero"`
	Gradient        Gradient   `json:"gradient,omitzero"`
	FontSize        Length     `json:"fontSize,omitzero"`
	LetterSpacing   Length     `json:"letterSpacing,omitzero"`
	FontFamily      FontFamily `json:"fontFamily,omitzero"`
	FontWeight      FontWeight `json:"fontWeight,omitzero"`
	LineHeight      LineHeight `json:"lineHeight,omitzero"`
	Padding         Sides      `json:"padding,omitzero"`
	Margin          Sides      `json:"margin,omitzero"`
	BlockGap        Length     `json:"blockGap,omitzero"`
	Border          Border     `json:"border,omitzero"`
}

// Every type, not only Style, needs UnmarshalJSON for json.Unmarshal to read it strictly.

func (x *Color) UnmarshalJSON(data []byte) error       { return decodeJSON(x, data) }
func (x *Length) UnmarshalJSON(data []byte) error      { return decodeJSON(x, data) }
func (x *Gradient) UnmarshalJSON(data []byte) error    { return decodeJSON(x, data) }
func (x *FontFamily) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }
func (x *FontWeight) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }
func (x *LineHeight) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }
func (x *BorderStyle) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }
func (x *Sides) UnmarshalJSON(data []byte) error       { return decodeJSON(x, data) }
func (x *Border) UnmarshalJSON(data []byte) error      { return decodeJSON(x, data) }
func (x *Style) UnmarshalJSON(data []byte) error       { return decodeJSON(x, data) }

func (x *Color) decode(d *decoder) error {
	var v Color
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "preset":
			v.Preset, err = d.string()
		case "value":
			v.Value, err = d.string()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Length) decode(d *decoder) error {
	var v Length
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "preset":
			v.Preset, err = d.string()
		case "value":
			v.Value, err = d.string()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Gradient) decode(d *decoder) error {
	var v Gradient
	err := d.object(func(key string) error {
		if key != "preset" {
			return d.errorf("unknown key")
		}
		var err error
		v.Preset, err = d.string()
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *FontFamily) decode(d *decoder) error {
	var v FontFamily
	err := d.object(func(key string) error {
		if key != "preset" {
			return d.errorf("unknown key")
		}
		var err error
		v.Preset, err = d.string()
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *FontWeight) decode(d *decoder) error {
	var v FontWeight
	err := d.object(func(key string) error {
		if key != "value" {
			return d.errorf("unknown key")
		}
		s, err := d.number()
		if err != nil {
			return err
		}
		v.Value, err = strconv.Atoi(s)
		if err != nil {
			return d.errorf("must be an integer")
		}
		// 0 would be indistinguishable from an omitted value.
		if err := checkFontWeight(v.Value); err != nil {
			return d.errorf("%v", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *LineHeight) decode(d *decoder) error {
	var v LineHeight
	err := d.object(func(key string) error {
		if key != "value" {
			return d.errorf("unknown key")
		}
		s, err := d.number()
		if err != nil {
			return err
		}
		v.Value, err = strconv.ParseFloat(s, 64)
		if err != nil {
			return d.errorf("invalid number")
		}
		// 0 would be indistinguishable from an omitted value.
		if err := checkLineHeight(v.Value); err != nil {
			return d.errorf("%v", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *BorderStyle) decode(d *decoder) error {
	var v BorderStyle
	err := d.object(func(key string) error {
		if key != "value" {
			return d.errorf("unknown key")
		}
		var err error
		v.Value, err = d.string()
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Sides) decode(d *decoder) error {
	var v Sides
	err := d.object(func(key string) error {
		switch key {
		case "top":
			return v.Top.decode(d)
		case "right":
			return v.Right.decode(d)
		case "bottom":
			return v.Bottom.decode(d)
		case "left":
			return v.Left.decode(d)
		default:
			return d.errorf("unknown key")
		}
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Border) decode(d *decoder) error {
	var v Border
	err := d.object(func(key string) error {
		switch key {
		case "width":
			return v.Width.decode(d)
		case "style":
			return v.Style.decode(d)
		case "color":
			return v.Color.decode(d)
		case "radius":
			return v.Radius.decode(d)
		default:
			return d.errorf("unknown key")
		}
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *Style) decode(d *decoder) error {
	var v Style
	err := d.object(func(key string) error {
		switch key {
		case "textColor":
			return v.TextColor.decode(d)
		case "backgroundColor":
			return v.BackgroundColor.decode(d)
		case "linkColor":
			return v.LinkColor.decode(d)
		case "gradient":
			return v.Gradient.decode(d)
		case "fontSize":
			return v.FontSize.decode(d)
		case "letterSpacing":
			return v.LetterSpacing.decode(d)
		case "fontFamily":
			return v.FontFamily.decode(d)
		case "fontWeight":
			return v.FontWeight.decode(d)
		case "lineHeight":
			return v.LineHeight.decode(d)
		case "padding":
			return v.Padding.decode(d)
		case "margin":
			return v.Margin.decode(d)
		case "blockGap":
			return v.BlockGap.decode(d)
		case "border":
			return v.Border.decode(d)
		default:
			return d.errorf("unknown key")
		}
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x Style) Validate() error {
	var c checker
	x.check(&c, "")
	return c.result()
}

func (x Style) check(c *checker, path string) {
	for _, v := range x.Values() {
		v.check(c, path+v.Path())
	}
}

// StyleValue is a value of a style, a color of a highlight or the height of a spacer.
type StyleValue struct {
	Item   string // the key in style, such as "padding"; "color" or "backgroundColor" of a highlight; "height" of a spacer
	Key    string // the side of padding and margin, such as "top", or the part of border, such as "width"; else ""
	Kind   string // the kind of presets it can name, as in theme.yaml, such as "color" or "spacing"; "" if none
	Preset string
	Value  string // written directly, as in CSS, such as "#1e73be", "-1rem", "700", "1.5" or "solid"
	format valueFormat
	length LengthRule // for formatLength
}

// valueFormat is how a StyleValue checks its Value.
type valueFormat int

const (
	formatNone valueFormat = iota // no Value: gradient and fontFamily have only presets
	formatColor
	formatLength
	formatFontWeight
	formatLineHeight
	formatBorderStyle
)

// Path returns the JSON Pointer of v from the style, or from the attrs of the highlight or of the spacer, such as
// "/padding/top".
func (v StyleValue) Path() string {
	if v.Key == "" {
		return "/" + v.Item
	}
	return "/" + v.Item + "/" + v.Key
}

// Check checks the format of v as validation does, but not against a theme, and returns the first problem: an *Error
// whose Path is relative to v, "" for both preset and value, "/preset" or "/value".
func (v StyleValue) Check() error {
	var c checker
	v.check(&c, "")
	if len(c.errs) == 0 {
		return nil
	}
	return c.errs[0]
}

func (v StyleValue) check(c *checker, path string) {
	if v.Preset != "" && v.Value != "" {
		c.addf(path, "set either preset or value, not both")
	}
	if v.Preset != "" {
		if v.Kind == "" {
			c.addf(path+"/preset", "presets are not allowed here")
		} else {
			c.add(path+"/preset", checkName(v.Preset))
		}
	}
	if v.Value != "" {
		c.add(path+"/value", v.checkValue())
	}
}

func (v StyleValue) checkValue() error {
	switch v.format {
	case formatColor:
		return checkColor(v.Value)
	case formatLength:
		return checkLength(v.Value, v.length)
	case formatFontWeight:
		n, err := strconv.Atoi(v.Value)
		if err != nil {
			return errors.New("must be an integer")
		}
		return checkFontWeight(n)
	case formatLineHeight:
		n, err := strconv.ParseFloat(v.Value, 64)
		if err != nil {
			return errors.New("invalid number")
		}
		return checkLineHeight(n)
	case formatBorderStyle:
		switch v.Value {
		case "solid", "dashed", "dotted":
			return nil
		}
		return fmt.Errorf("unknown border style %q", v.Value)
	case formatNone:
	}
	return errors.New("values are not allowed here")
}

func (x Color) value(item, key string) StyleValue {
	return StyleValue{Item: item, Key: key, Kind: "color", Preset: x.Preset, Value: x.Value, format: formatColor}
}

func (x Length) value(item, key, kind string, r LengthRule) StyleValue {
	return StyleValue{Item: item, Key: key, Kind: kind, Preset: x.Preset, Value: x.Value, format: formatLength, length: r}
}

// Values returns the values that x sets, in the order of the items and, within padding, margin and border, of their
// keys.
func (x Style) Values() []StyleValue {
	var vs []StyleValue
	add := func(set bool, v StyleValue) {
		if set {
			vs = append(vs, v)
		}
	}
	color := func(item, key string, c Color) {
		add(c != (Color{}), c.value(item, key))
	}
	length := func(item, key, kind string, l Length, r LengthRule) {
		add(l != (Length{}), l.value(item, key, kind, r))
	}
	sides := func(item string, s Sides, r LengthRule) {
		length(item, "top", "spacing", s.Top, r)
		length(item, "right", "spacing", s.Right, r)
		length(item, "bottom", "spacing", s.Bottom, r)
		length(item, "left", "spacing", s.Left, r)
	}
	color("textColor", "", x.TextColor)
	color("backgroundColor", "", x.BackgroundColor)
	color("linkColor", "", x.LinkColor)
	add(x.Gradient.Preset != "", StyleValue{Item: "gradient", Kind: "gradient", Preset: x.Gradient.Preset})
	length("fontSize", "", "fontSize", x.FontSize, LengthRule{Preset: true, Percent: true})
	length("letterSpacing", "", "", x.LetterSpacing, LengthRule{Negative: true})
	add(x.FontFamily.Preset != "", StyleValue{Item: "fontFamily", Kind: "fontFamily", Preset: x.FontFamily.Preset})
	// Check reads the numbers back from Value, so they are written exactly.
	add(x.FontWeight.Value != 0, StyleValue{
		Item: "fontWeight", Value: strconv.Itoa(x.FontWeight.Value), format: formatFontWeight,
	})
	add(x.LineHeight.Value != 0, StyleValue{
		Item: "lineHeight", Value: strconv.FormatFloat(x.LineHeight.Value, 'f', -1, 64), format: formatLineHeight,
	})
	sides("padding", x.Padding, LengthRule{Preset: true, Percent: true})
	sides("margin", x.Margin, LengthRule{Preset: true, Negative: true, Percent: true})
	length("blockGap", "", "spacing", x.BlockGap, LengthRule{Preset: true, Percent: true})
	length("border", "width", "", x.Border.Width, LengthRule{})
	add(x.Border.Style.Value != "", StyleValue{
		Item: "border", Key: "style", Value: x.Border.Style.Value, format: formatBorderStyle,
	})
	color("border", "color", x.Border.Color)
	length("border", "radius", "", x.Border.Radius, LengthRule{Percent: true})
	return vs
}

// item returns the value of the item name, such as x.Padding for "padding", or nil for an unknown name.
func (x *Style) item(name string) any {
	switch name {
	case "textColor":
		return x.TextColor
	case "backgroundColor":
		return x.BackgroundColor
	case "linkColor":
		return x.LinkColor
	case "gradient":
		return x.Gradient
	case "fontSize":
		return x.FontSize
	case "letterSpacing":
		return x.LetterSpacing
	case "fontFamily":
		return x.FontFamily
	case "fontWeight":
		return x.FontWeight
	case "lineHeight":
		return x.LineHeight
	case "padding":
		return x.Padding
	case "margin":
		return x.Margin
	case "blockGap":
		return x.BlockGap
	case "border":
		return x.Border
	}
	return nil
}
