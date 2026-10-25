package content

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
)

type InlineType string

const (
	InlineText      InlineType = "text"
	InlineHardBreak InlineType = "hardBreak"
)

type Inline struct {
	Type  InlineType `json:"type"`
	Text  string     `json:"text,omitempty"`
	Marks []Mark     `json:"marks,omitempty"`
}

type MarkType string

const (
	MarkBold        MarkType = "bold"
	MarkItalic      MarkType = "italic"
	MarkUnderline   MarkType = "underline"
	MarkStrike      MarkType = "strike"
	MarkCode        MarkType = "code"
	MarkSubscript   MarkType = "subscript"
	MarkSuperscript MarkType = "superscript"
	MarkHighlight   MarkType = "highlight"
	MarkLink        MarkType = "link"
)

// Mark is an inline mark; Highlight and Link are the attrs of those types and nil for the others.
type Mark struct {
	Type      MarkType
	Highlight *Highlight
	Link      *Link
}

type Highlight struct {
	Variant         string `json:"variant,omitempty"`
	Color           Color  `json:"color,omitzero"`
	BackgroundColor Color  `json:"backgroundColor,omitzero"`
}

type Rel string

const (
	RelNofollow  Rel = "nofollow"
	RelSponsored Rel = "sponsored"
	RelUGC       Rel = "ugc"
)

type Link struct {
	Href   string `json:"href,omitempty"`
	Entry  string `json:"entry,omitempty"`
	Media  string `json:"media,omitempty"`
	Anchor string `json:"anchor,omitempty"`
	NewTab bool   `json:"newTab,omitzero"`
	Rel    []Rel  `json:"rel,omitempty"`
}

// RichText is text with inline marks, the same as a richText field.
type RichText []Inline

func (x *Inline) UnmarshalJSON(data []byte) error    { return decodeJSON(x, data) }
func (x *Mark) UnmarshalJSON(data []byte) error      { return decodeJSON(x, data) }
func (x *Highlight) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }
func (x *Link) UnmarshalJSON(data []byte) error      { return decodeJSON(x, data) }
func (x *RichText) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }

func (x *Inline) decode(d *decoder) error {
	var v Inline
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "type":
			var s string
			s, err = d.string()
			v.Type = InlineType(s)
		case "text":
			v.Text, err = d.string()
		case "marks":
			err = d.array(func(int) error {
				var m Mark
				if err := m.decode(d); err != nil {
					return err
				}
				v.Marks = append(v.Marks, m)
				return nil
			})
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	switch v.Type {
	case "":
		return d.errorf(`missing key "type"`)
	case InlineText:
		if v.Text == "" {
			return d.errorf(`missing key "text"`)
		}
	case InlineHardBreak:
		if v.Text != "" {
			return &Error{Path: d.pointer() + "/text", Msg: "not allowed for hardBreak"}
		}
		if v.Marks != nil {
			return &Error{Path: d.pointer() + "/marks", Msg: "not allowed for hardBreak"}
		}
	default:
		return &Error{Path: d.pointer() + "/type", Msg: fmt.Sprintf("unknown inline type %q", v.Type)}
	}
	*x = v
	return nil
}

func (x *Mark) decode(d *decoder) error {
	var v Mark
	var attrs jsontext.Value
	var attrsPath string
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "type":
			var s string
			s, err = d.string()
			v.Type = MarkType(s)
		case "attrs":
			// attrs may come before type, so it is decoded once type is known.
			attrs, attrsPath, err = d.raw()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	switch v.Type {
	case "":
		return d.errorf(`missing key "type"`)
	case MarkBold, MarkItalic, MarkUnderline, MarkStrike, MarkCode, MarkSubscript, MarkSuperscript:
		if attrs != nil {
			return &Error{Path: attrsPath, Msg: fmt.Sprintf("not allowed for %s", v.Type)}
		}
	case MarkHighlight:
		if attrs == nil {
			return d.errorf(`missing key "attrs"`)
		}
		var h Highlight
		if err := unmarshal(attrs, attrsPath, h.decode); err != nil {
			return err
		}
		v.Highlight = &h
	case MarkLink:
		if attrs == nil {
			return d.errorf(`missing key "attrs"`)
		}
		var l Link
		if err := unmarshal(attrs, attrsPath, l.decode); err != nil {
			return err
		}
		v.Link = &l
	default:
		return &Error{Path: d.pointer() + "/type", Msg: fmt.Sprintf("unknown mark type %q", v.Type)}
	}
	*x = v
	return nil
}

func (x *Highlight) decode(d *decoder) error {
	var v Highlight
	err := d.object(func(key string) error {
		switch key {
		case "variant":
			var err error
			v.Variant, err = d.string()
			return err
		case "color":
			return v.Color.decode(d)
		case "backgroundColor":
			return v.BackgroundColor.decode(d)
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

func (x *Link) decode(d *decoder) error {
	var v Link
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "href":
			v.Href, err = d.string()
		case "entry":
			v.Entry, err = d.string()
		case "media":
			v.Media, err = d.string()
		case "anchor":
			v.Anchor, err = d.string()
		case "newTab":
			v.NewTab, err = d.bool()
		case "rel":
			err = d.array(func(int) error {
				s, err := d.string()
				if err != nil {
					return err
				}
				v.Rel = append(v.Rel, Rel(s))
				return nil
			})
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

func (x *RichText) decode(d *decoder) error {
	var v RichText
	err := d.items(func(int) error {
		var n Inline
		if err := n.decode(d); err != nil {
			return err
		}
		v = append(v, n)
		return nil
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x Mark) MarshalJSON() ([]byte, error) {
	// A nil pointer in attrs would be written as "attrs":null.
	var attrs any
	switch x.Type {
	case MarkHighlight:
		if x.Highlight != nil {
			attrs = x.Highlight
		}
	case MarkLink:
		if x.Link != nil {
			attrs = x.Link
		}
	case MarkBold, MarkItalic, MarkUnderline, MarkStrike, MarkCode, MarkSubscript, MarkSuperscript:
	}
	return json.Marshal(struct {
		Type  MarkType `json:"type"`
		Attrs any      `json:"attrs,omitzero"`
	}{Type: x.Type, Attrs: attrs})
}

func (x RichText) MarshalJSON() ([]byte, error) {
	// Never null, which UnmarshalJSON rejects.
	if len(x) == 0 {
		return []byte("[]"), nil
	}
	// As a RichText, x would call this method again.
	return json.Marshal([]Inline(x))
}

// Normalize cleans up texts and hrefs in place, also through the Link pointers, and removes empty texts.
func (x *RichText) Normalize() {
	rt := *x
	for i := range rt {
		n := &rt[i]
		if n.Type != InlineText {
			continue
		}
		n.Text = normalizeText(n.Text, false)
		for _, m := range n.Marks {
			if m.Type == MarkLink && m.Link != nil && m.Link.Href != "" {
				m.Link.Href = normalizeHref(m.Link.Href)
			}
		}
	}
	rt = slices.DeleteFunc(rt, func(n Inline) bool { return n.Type == InlineText && n.Text == "" })
	// Empty is nil, as UnmarshalJSON makes it.
	if len(rt) == 0 {
		rt = nil
	}
	*x = rt
}

func (x RichText) Validate() error {
	var c checker
	x.check(&c, "")
	return c.result()
}

func (x RichText) check(c *checker, path string) {
	for i, n := range x {
		n.check(c, path+"/"+strconv.Itoa(i))
	}
}

func (x Inline) check(c *checker, path string) {
	switch x.Type {
	case InlineText:
		if x.Text == "" {
			c.addf(path+"/text", "empty text")
		} else {
			c.add(path+"/text", checkText(x.Text, false))
		}
		for j, m := range x.Marks {
			m.check(c, path+"/marks/"+strconv.Itoa(j), x.Marks[:j])
		}
	case InlineHardBreak:
		if x.Text != "" {
			c.addf(path+"/text", "not allowed for hardBreak")
		}
		if len(x.Marks) > 0 {
			c.addf(path+"/marks", "not allowed for hardBreak")
		}
	default:
		c.addf(path+"/type", "unknown inline type %q", x.Type)
	}
}

// check reports the problems in x, given the marks before it on the same text.
func (x Mark) check(c *checker, path string, before []Mark) {
	if !x.Type.known() {
		c.addf(path+"/type", "unknown mark type %q", x.Type)
		return
	}
	has := func(t MarkType) bool {
		return slices.ContainsFunc(before, func(m Mark) bool { return m.Type == t })
	}
	switch {
	case has(x.Type):
		c.addf(path, "duplicate %s mark", x.Type)
	case x.Type == MarkSubscript && has(MarkSuperscript), x.Type == MarkSuperscript && has(MarkSubscript):
		c.addf(path, "subscript and superscript cannot be combined")
	}
	switch x.Type {
	case MarkLink:
		if x.Link == nil {
			c.addf(path+"/attrs", "missing attributes")
		}
		if x.Highlight != nil {
			c.addf(path+"/attrs", "highlight attributes are not allowed for link")
		}
		if x.Link != nil {
			x.Link.check(c, path+"/attrs")
		}
	case MarkHighlight:
		if x.Highlight == nil {
			c.addf(path+"/attrs", "missing attributes")
		}
		if x.Link != nil {
			c.addf(path+"/attrs", "link attributes are not allowed for highlight")
		}
		if x.Highlight != nil {
			x.Highlight.check(c, path+"/attrs")
		}
	case MarkBold, MarkItalic, MarkUnderline, MarkStrike, MarkCode, MarkSubscript, MarkSuperscript:
		if x.Link != nil || x.Highlight != nil {
			c.addf(path+"/attrs", "not allowed for %s", x.Type)
		}
	}
}

func (x MarkType) known() bool {
	switch x {
	case MarkBold, MarkItalic, MarkUnderline, MarkStrike, MarkCode, MarkSubscript, MarkSuperscript, MarkHighlight, MarkLink:
		return true
	}
	return false
}

func (x Link) check(c *checker, path string) {
	targets := 0
	for _, s := range []string{x.Href, x.Entry, x.Media} {
		if s != "" {
			targets++
		}
	}
	switch {
	case targets > 1:
		c.addf(path, "set at most one of href, entry and media")
	case targets == 0 && x.Anchor == "":
		c.addf(path, "link needs href, entry, media or anchor")
	}
	if x.Anchor != "" && x.Href != "" {
		c.addf(path+"/anchor", "anchor cannot be combined with href; put the fragment in href")
	}
	if x.Anchor != "" && x.Media != "" {
		c.addf(path+"/anchor", "anchor cannot be combined with media")
	}
	if x.Href != "" {
		c.add(path+"/href", checkHref(x.Href))
	}
	if x.Entry != "" {
		c.add(path+"/entry", checkName(x.Entry))
	}
	if x.Media != "" {
		c.add(path+"/media", checkName(x.Media))
	}
	if x.Anchor != "" {
		c.add(path+"/anchor", checkName(x.Anchor))
	}
	for k, r := range x.Rel {
		p := path + "/rel/" + strconv.Itoa(k)
		switch r {
		case RelNofollow, RelSponsored, RelUGC:
			if slices.Contains(x.Rel[:k], r) {
				c.addf(p, "duplicate rel %q", r)
			}
		default:
			c.addf(p, "unknown rel %q", r)
		}
	}
}

func (x Highlight) check(c *checker, path string) {
	colored := x.Color != (Color{}) || x.BackgroundColor != (Color{})
	switch {
	case x.Variant != "" && colored:
		c.addf(path, "variant cannot be combined with color or backgroundColor")
	case x.Variant == "" && !colored:
		c.addf(path, "highlight needs variant, color or backgroundColor")
	}
	if x.Variant != "" {
		c.add(path+"/variant", checkName(x.Variant))
	}
	x.Color.check(c, path+"/color")
	x.BackgroundColor.check(c, path+"/backgroundColor")
}
