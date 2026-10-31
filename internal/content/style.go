package content

import "strconv"

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
	x.TextColor.check(c, path+"/textColor")
	x.BackgroundColor.check(c, path+"/backgroundColor")
	x.LinkColor.check(c, path+"/linkColor")
	x.Gradient.check(c, path+"/gradient")
	x.FontSize.check(c, path+"/fontSize", LengthRule{Preset: true, Percent: true})
	x.LetterSpacing.check(c, path+"/letterSpacing", LengthRule{Negative: true})
	x.FontFamily.check(c, path+"/fontFamily")
	x.FontWeight.check(c, path+"/fontWeight")
	x.LineHeight.check(c, path+"/lineHeight")
	x.Padding.check(c, path+"/padding", LengthRule{Preset: true, Percent: true})
	x.Margin.check(c, path+"/margin", LengthRule{Preset: true, Negative: true, Percent: true})
	x.BlockGap.check(c, path+"/blockGap", LengthRule{Preset: true, Percent: true})
	x.Border.check(c, path+"/border")
}

func (x Color) check(c *checker, path string) {
	if x.Preset != "" && x.Value != "" {
		c.addf(path, "set either preset or value, not both")
	}
	if x.Preset != "" {
		c.add(path+"/preset", checkName(x.Preset))
	}
	if x.Value != "" {
		c.add(path+"/value", checkColor(x.Value))
	}
}

func (x Length) check(c *checker, path string, r LengthRule) {
	if x.Preset != "" && x.Value != "" {
		c.addf(path, "set either preset or value, not both")
	}
	if x.Preset != "" {
		if r.Preset {
			c.add(path+"/preset", checkName(x.Preset))
		} else {
			c.addf(path+"/preset", "presets are not allowed here")
		}
	}
	if x.Value != "" {
		c.add(path+"/value", checkLength(x.Value, r))
	}
}

func (x Gradient) check(c *checker, path string) {
	if x.Preset != "" {
		c.add(path+"/preset", checkName(x.Preset))
	}
}

func (x FontFamily) check(c *checker, path string) {
	if x.Preset != "" {
		c.add(path+"/preset", checkName(x.Preset))
	}
}

func (x FontWeight) check(c *checker, path string) {
	if x.Value != 0 {
		c.add(path+"/value", checkFontWeight(x.Value))
	}
}

func (x LineHeight) check(c *checker, path string) {
	if x.Value != 0 {
		c.add(path+"/value", checkLineHeight(x.Value))
	}
}

func (x BorderStyle) check(c *checker, path string) {
	switch x.Value {
	case "", "solid", "dashed", "dotted":
	default:
		c.addf(path+"/value", "unknown border style %q", x.Value)
	}
}

func (x Sides) check(c *checker, path string, r LengthRule) {
	x.Top.check(c, path+"/top", r)
	x.Right.check(c, path+"/right", r)
	x.Bottom.check(c, path+"/bottom", r)
	x.Left.check(c, path+"/left", r)
}

func (x Border) check(c *checker, path string) {
	x.Width.check(c, path+"/width", LengthRule{})
	x.Style.check(c, path+"/style")
	x.Color.check(c, path+"/color")
	x.Radius.check(c, path+"/radius", LengthRule{Percent: true})
}
