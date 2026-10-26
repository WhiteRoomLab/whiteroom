package content

import (
	"fmt"
	"slices"
	"strings"
)

// Attrs holds the attributes of every block type; blockSpecs lists the keys each type may use.
type Attrs struct {
	Level         int      `json:"level,omitzero"`
	Ordered       bool     `json:"ordered,omitzero"`
	Start         int      `json:"start,omitzero"`
	Citation      RichText `json:"citation,omitempty"`
	Layout        Layout   `json:"layout,omitzero"`
	TagName       string   `json:"tagName,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	URL           string   `json:"url,omitempty"`
	Media         string   `json:"media,omitempty"`
	Alt           string   `json:"alt,omitempty"`
	Decorative    bool     `json:"decorative,omitzero"`
	Label         string   `json:"label,omitempty"`
	Caption       RichText `json:"caption,omitempty"`
	Link          *Link    `json:"link,omitempty"`
	Size          string   `json:"size,omitempty"`
	Width         string   `json:"width,omitempty"`
	AspectRatio   string   `json:"aspectRatio,omitempty"`
	Align         string   `json:"align,omitempty"`
	VerticalAlign string   `json:"verticalAlign,omitempty"`
	StackOnMobile *bool    `json:"stackOnMobile,omitempty"` // nil is omitted, which means true
	Section       string   `json:"section,omitempty"`
	Header        bool     `json:"header,omitzero"`
	Colspan       int      `json:"colspan,omitzero"`
	Rowspan       int      `json:"rowspan,omitzero"`
	TextAlign     string   `json:"textAlign,omitempty"`
	Justify       string   `json:"justify,omitempty"`
	Height        Length   `json:"height,omitzero"`
	Language      string   `json:"language,omitempty"`
	Form          string   `json:"form,omitempty"`
	HTML          string   `json:"html,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

type Layout struct {
	Type           string `json:"type,omitempty"`
	Justify        string `json:"justify,omitempty"`
	VerticalAlign  string `json:"verticalAlign,omitempty"`
	NoWrap         bool   `json:"nowrap,omitzero"`
	Columns        int    `json:"columns,omitzero"`
	MinColumnWidth string `json:"minColumnWidth,omitempty"`
}

func (x *Attrs) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }
func (x *Layout) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Attrs) decode(d *decoder) error { return x.decodeFor(d, "") }

var attrKeys = func() map[string]bool {
	keys := make(map[string]bool)
	for _, s := range blockSpecs {
		for _, k := range s.attrs {
			keys[k] = true
		}
	}
	return keys
}()

// decodeFor reads the attrs of a block of type t, or of any type if t is "".
func (x *Attrs) decodeFor(d *decoder, t BlockType) error {
	allowed := blockSpecs[t].attrs
	if t != "" && len(allowed) == 0 {
		return d.errorf("not allowed for %s", t)
	}
	var v Attrs
	err := d.object(func(key string) error {
		if t != "" && !slices.Contains(allowed, key) {
			if attrKeys[key] {
				return d.errorf("not allowed for %s", t)
			}
			return d.errorf("unknown key")
		}
		var err error
		switch key {
		case "level":
			v.Level, err = readInt(d, checkLevel)
		case "ordered":
			v.Ordered, err = d.bool()
		case "start":
			v.Start, err = readInt(d, checkStart)
		case "citation":
			err = v.Citation.decode(d)
		case "layout":
			err = v.Layout.decode(d)
		case "tagName":
			v.TagName, err = d.string()
		case "provider":
			v.Provider, err = d.string()
		case "url":
			v.URL, err = d.string()
		case "media":
			v.Media, err = d.string()
		case "alt":
			v.Alt, err = d.string()
		case "decorative":
			v.Decorative, err = d.bool()
		case "label":
			v.Label, err = d.string()
		case "caption":
			err = v.Caption.decode(d)
		case "link":
			v.Link = new(Link)
			err = v.Link.decode(d)
		case "size":
			v.Size, err = d.string()
		case "width":
			v.Width, err = d.string()
		case "aspectRatio":
			v.AspectRatio, err = d.string()
		case "align":
			v.Align, err = d.string()
		case "verticalAlign":
			v.VerticalAlign, err = d.string()
		case "stackOnMobile":
			var b bool
			b, err = d.bool()
			v.StackOnMobile = &b
		case "section":
			v.Section, err = d.string()
		case "header":
			v.Header, err = d.bool()
		case "colspan":
			v.Colspan, err = readInt(d, func(n int) error { return checkSpan("colspan", n) })
		case "rowspan":
			v.Rowspan, err = readInt(d, func(n int) error { return checkSpan("rowspan", n) })
		case "textAlign":
			v.TextAlign, err = d.string()
		case "justify":
			v.Justify, err = d.string()
		case "height":
			err = v.Height.decode(d)
		case "language":
			v.Language, err = d.string()
		case "form":
			v.Form, err = d.string()
		case "html":
			v.HTML, err = d.string()
		case "reason":
			v.Reason, err = d.string()
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

func (x *Layout) decode(d *decoder) error {
	var v Layout
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "type":
			v.Type, err = d.string()
		case "justify":
			v.Justify, err = d.string()
		case "verticalAlign":
			v.VerticalAlign, err = d.string()
		case "nowrap":
			v.NoWrap, err = d.bool()
		case "columns":
			v.Columns, err = readInt(d, checkGridColumns)
		case "minColumnWidth":
			v.MinColumnWidth, err = d.string()
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

// readInt reads an integer in the range that check accepts.
func readInt(d *decoder, check func(int) error) (int, error) {
	v, err := d.int()
	if err != nil {
		return 0, err
	}
	// 0 would be indistinguishable from an omitted value.
	if err := check(v); err != nil {
		return 0, d.errorf("%v", err)
	}
	return v, nil
}

// keys returns the JSON keys of the attributes that are set, in the order of the fields.
func (x Attrs) keys() []string {
	var ks []string
	add := func(set bool, key string) {
		if set {
			ks = append(ks, key)
		}
	}
	add(x.Level != 0, "level")
	add(x.Ordered, "ordered")
	add(x.Start != 0, "start")
	add(len(x.Citation) > 0, "citation")
	add(x.Layout != (Layout{}), "layout")
	add(x.TagName != "", "tagName")
	add(x.Provider != "", "provider")
	add(x.URL != "", "url")
	add(x.Media != "", "media")
	add(x.Alt != "", "alt")
	add(x.Decorative, "decorative")
	add(x.Label != "", "label")
	add(len(x.Caption) > 0, "caption")
	add(x.Link != nil, "link")
	add(x.Size != "", "size")
	add(x.Width != "", "width")
	add(x.AspectRatio != "", "aspectRatio")
	add(x.Align != "", "align")
	add(x.VerticalAlign != "", "verticalAlign")
	add(x.StackOnMobile != nil, "stackOnMobile")
	add(x.Section != "", "section")
	add(x.Header, "header")
	add(x.Colspan != 0, "colspan")
	add(x.Rowspan != 0, "rowspan")
	add(x.TextAlign != "", "textAlign")
	add(x.Justify != "", "justify")
	add(x.Height != (Length{}), "height")
	add(x.Language != "", "language")
	add(x.Form != "", "form")
	add(x.HTML != "", "html")
	add(x.Reason != "", "reason")
	return ks
}

func (x *Attrs) normalize() {
	x.Citation.Normalize()
	x.Caption.Normalize()
	x.Alt = normalizeText(x.Alt, false)
	x.Label = normalizeText(x.Label, false)
	x.Reason = normalizeText(x.Reason, false)
	// No NFC, which would break URLs in src and href.
	x.HTML = cleanText(x.HTML, true)
	if x.URL != "" {
		x.URL = normalizeHref(x.URL)
	}
	if x.Link != nil && x.Link.Href != "" {
		x.Link.Href = normalizeHref(x.Link.Href)
	}
}

var textAligns = []string{"left", "center", "right"}

// embedProviderNames are the keys of embedProviders, in the order that messages list them.
var embedProviderNames = []string{"youtube", "vimeo", "x", "googleMaps"}

func (x *Block) checkAttrs(c *checker, path string) {
	a := &x.Attrs
	choice := func(key, v string, choices ...string) {
		if v != "" {
			c.add(path+"/"+key, checkChoice(key, v, choices...))
		}
	}
	switch x.Type {
	case BlockParagraph:
		choice("textAlign", a.TextAlign, textAligns...)
	case BlockHeading:
		if a.Level == 0 {
			c.addf(path+"/level", "missing level")
		} else {
			c.add(path+"/level", checkLevel(a.Level))
		}
		choice("textAlign", a.TextAlign, textAligns...)
	case BlockList:
		if a.Start != 0 {
			if !a.Ordered {
				c.addf(path+"/start", "start needs ordered")
			}
			c.add(path+"/start", checkStart(a.Start))
		}
	case BlockQuote:
		a.Citation.check(c, path+"/citation")
	case BlockImage:
		if a.Media == "" {
			c.addf(path+"/media", "missing media")
		} else {
			c.media(path+"/media", a.Media, MediaImage)
		}
		switch {
		case a.Alt != "" && a.Decorative:
			c.addf(path+"/alt", "alt cannot be combined with decorative")
		case a.Alt == "" && !a.Decorative && !c.doc.opts.Import && !c.keptWithoutAlt(x):
			c.addf(path+"/alt", "missing alt; set decorative for a decorative image")
		}
		if a.Alt != "" {
			c.add(path+"/alt", checkLine(a.Alt))
		}
		a.Caption.check(c, path+"/caption")
		if a.Link != nil {
			a.Link.check(c, path+"/link")
		}
		if a.Size != "" {
			c.add(path+"/size", checkName(a.Size))
		}
		if a.Width != "" {
			c.add(path+"/width", checkLength(a.Width, lengthRule{percent: true}))
		}
		if a.AspectRatio != "" {
			c.add(path+"/aspectRatio", checkAspectRatio(a.AspectRatio))
		}
		choice("align", a.Align, "left", "center", "right", "wide", "full")
	case BlockTable:
		a.Caption.check(c, path+"/caption")
	case BlockTableRow:
		choice("section", a.Section, "head", "body", "foot")
	case BlockTableCell:
		if a.Colspan != 0 {
			c.add(path+"/colspan", checkSpan("colspan", a.Colspan))
		}
		if a.Rowspan != 0 {
			c.add(path+"/rowspan", checkSpan("rowspan", a.Rowspan))
		}
		choice("textAlign", a.TextAlign, textAligns...)
	case BlockButtons:
		choice("justify", a.Justify, "left", "center", "right", "spaceBetween")
	case BlockButton:
		if a.Label == "" {
			c.addf(path+"/label", "missing label")
		} else {
			c.add(path+"/label", checkLine(a.Label))
		}
		if a.Link != nil {
			a.Link.check(c, path+"/link")
		}
		choice("width", a.Width, "25%", "50%", "75%", "100%")
	case BlockSpacer:
		if a.Height == (Length{}) {
			c.addf(path+"/height", "missing height")
		} else {
			a.Height.check(c, path+"/height", lengthRule{preset: true})
		}
	case BlockCode:
		if a.Language != "" {
			c.add(path+"/language", checkName(a.Language))
		}
	case BlockEmbed:
		if a.Provider == "" {
			c.addf(path+"/provider", "missing provider")
		} else {
			c.add(path+"/provider", checkChoice("provider", a.Provider, embedProviderNames...))
		}
		if a.URL == "" {
			c.addf(path+"/url", "missing url")
		} else {
			// checkEmbedURL leaves credentials, backslashes and control characters to checkHref.
			err := checkHref(a.URL)
			if err == nil && slices.Contains(embedProviderNames, a.Provider) {
				err = checkEmbedURL(a.Provider, a.URL)
			}
			c.add(path+"/url", err)
		}
		a.Caption.check(c, path+"/caption")
	case BlockColumns:
		choice("verticalAlign", a.VerticalAlign, "top", "center", "bottom")
	case BlockColumn:
		if a.Width != "" {
			c.add(path+"/width", checkLength(a.Width, lengthRule{percent: true}))
		}
		choice("verticalAlign", a.VerticalAlign, "top", "center", "bottom", "stretch")
	case BlockGroup:
		if a.Layout != (Layout{}) {
			a.Layout.check(c, path+"/layout")
		}
		choice("tagName", a.TagName, "div", "section", "article", "aside")
		if a.Label != "" {
			c.add(path+"/label", checkLine(a.Label))
		}
		choice("align", a.Align, "wide", "full")
	case BlockFile:
		if a.Media == "" {
			c.addf(path+"/media", "missing media")
		} else {
			c.media(path+"/media", a.Media)
		}
		if a.Label != "" {
			c.add(path+"/label", checkLine(a.Label))
		}
	case BlockForm:
		if a.Form == "" {
			c.addf(path+"/form", "missing form")
		} else {
			c.ref(path+"/form", RefForm, a.Form)
		}
	case BlockHTML:
		if a.HTML == "" {
			c.addf(path+"/html", "missing html")
		} else {
			c.add(path+"/html", checkRaw(a.HTML))
		}
	case BlockLegacy:
		if a.HTML == "" {
			c.addf(path+"/html", "missing html")
		} else {
			c.add(path+"/html", checkRaw(a.HTML))
		}
		if a.Reason != "" {
			c.add(path+"/reason", checkLine(a.Reason))
		}
	case BlockListItem, BlockSeparator:
	}
}

func (x Layout) check(c *checker, path string) {
	if x.Type == "" {
		c.addf(path+"/type", "missing layout type")
		return
	}
	if err := checkChoice("type", x.Type, "flow", "row", "stack", "grid"); err != nil {
		c.add(path+"/type", err)
		return
	}
	// allowed reports whether the type can use key, and adds a problem if not.
	allowed := func(key string, types ...string) bool {
		if slices.Contains(types, x.Type) {
			return true
		}
		c.addf(path+"/"+key, "not allowed for %s layout", x.Type)
		return false
	}
	var justify, verticalAlign []string
	switch x.Type {
	case "row":
		justify = []string{"left", "center", "right", "spaceBetween"}
		verticalAlign = []string{"top", "center", "bottom", "stretch"}
	case "stack":
		justify = []string{"left", "center", "right", "stretch"}
		verticalAlign = []string{"top", "center", "bottom", "spaceBetween"}
	}
	if x.Justify != "" && allowed("justify", "row", "stack") {
		c.add(path+"/justify", checkChoice("justify", x.Justify, justify...))
	}
	if x.VerticalAlign != "" && allowed("verticalAlign", "row", "stack") {
		c.add(path+"/verticalAlign", checkChoice("verticalAlign", x.VerticalAlign, verticalAlign...))
	}
	if x.NoWrap {
		allowed("nowrap", "row")
	}
	if x.Columns != 0 && allowed("columns", "grid") {
		c.add(path+"/columns", checkGridColumns(x.Columns))
	}
	if x.MinColumnWidth != "" && allowed("minColumnWidth", "grid") {
		c.add(path+"/minColumnWidth", checkLength(x.MinColumnWidth, lengthRule{}))
	}
}

func checkLevel(v int) error {
	if v < 2 || v > 6 {
		return fmt.Errorf("level %d is out of range (2-6)", v)
	}
	return nil
}

func checkStart(v int) error {
	if v < 1 {
		return fmt.Errorf("start %d is out of range (1 or more)", v)
	}
	return nil
}

func checkSpan(name string, v int) error {
	if v < 1 || v > 1000 {
		return fmt.Errorf("%s %d is out of range (1-1000)", name, v)
	}
	return nil
}

func checkGridColumns(v int) error {
	if v < 1 || v > 16 {
		return fmt.Errorf("columns %d is out of range (1-16)", v)
	}
	return nil
}

func checkChoice(name, v string, choices ...string) error {
	if !slices.Contains(choices, v) {
		return fmt.Errorf("unknown %s %q (use %s)", name, v, strings.Join(choices, ", "))
	}
	return nil
}

// checkAspectRatio accepts a positive number, such as "1.5", or a ratio of two, such as "16/9".
func checkAspectRatio(s string) error {
	positive := func(s string) bool {
		v, ok := parseNumber(s)
		return ok && v > 0
	}
	w, h, ok := strings.Cut(s, "/")
	if !ok {
		h = "1"
	}
	if !positive(w) || !positive(h) {
		return fmt.Errorf("invalid aspect ratio %q", s)
	}
	return nil
}
