package content

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fullAttrsJSON is fullAttrs, which sets every attribute, in field order.
const fullAttrsJSON = `{
  "level": 2,
  "ordered": true,
  "start": 3,
  "citation": [{"type": "text", "text": "A customer"}],
  "layout": {"type": "grid", "columns": 3, "minColumnWidth": "12rem"},
  "tagName": "section",
  "provider": "youtube",
  "url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  "media": "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5",
  "alt": "Our office building",
  "decorative": true,
  "label": "Notice",
  "caption": [{"type": "text", "text": "Head office"}],
  "link": {"href": "https://example.com/office", "newTab": true},
  "size": "large",
  "width": "300px",
  "aspectRatio": "16/9",
  "align": "wide",
  "verticalAlign": "top",
  "stackOnMobile": false,
  "section": "head",
  "header": true,
  "colspan": 2,
  "rowspan": 3,
  "textAlign": "center",
  "justify": "spaceBetween",
  "height": {"preset": "large"},
  "language": "go",
  "form": "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6",
  "html": "<p>Custom</p>",
  "reason": "shortcode gallery"
}`

var fullAttrs = Attrs{
	Level:         2,
	Ordered:       true,
	Start:         3,
	Citation:      RichText{{Type: InlineText, Text: "A customer"}},
	Layout:        Layout{Type: "grid", Columns: 3, MinColumnWidth: "12rem"},
	TagName:       "section",
	Provider:      "youtube",
	URL:           "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	Media:         "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5",
	Alt:           "Our office building",
	Decorative:    true,
	Label:         "Notice",
	Caption:       RichText{{Type: InlineText, Text: "Head office"}},
	Link:          &Link{Href: "https://example.com/office", NewTab: true},
	Size:          "large",
	Width:         "300px",
	AspectRatio:   "16/9",
	Align:         "wide",
	VerticalAlign: "top",
	StackOnMobile: new(false),
	Section:       "head",
	Header:        true,
	Colspan:       2,
	Rowspan:       3,
	TextAlign:     "center",
	Justify:       "spaceBetween",
	Height:        Length{Preset: "large"},
	Language:      "go",
	Form:          "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6",
	HTML:          "<p>Custom</p>",
	Reason:        "shortcode gallery",
}

func TestAttrsDecodeFor(t *testing.T) {
	headOffice := RichText{{Type: InlineText, Text: "Head office"}}
	tests := []struct {
		name string
		typ  BlockType
		in   string
		want Attrs
	}{
		{name: "paragraph", typ: BlockParagraph, in: `{"textAlign":"center"}`, want: Attrs{TextAlign: "center"}},
		{name: "heading", typ: BlockHeading, in: `{"level":6,"textAlign":"right"}`, want: Attrs{Level: 6, TextAlign: "right"}},
		{name: "list", typ: BlockList, in: `{"ordered":true,"start":3}`, want: Attrs{Ordered: true, Start: 3}},
		{
			name: "quote",
			typ:  BlockQuote,
			in:   `{"citation":[{"type":"text","text":"A customer"}]}`,
			want: Attrs{Citation: RichText{{Type: InlineText, Text: "A customer"}}},
		},
		{
			name: "image",
			typ:  BlockImage,
			in: `{
  "media": "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", "alt": "Our office building", "decorative": true,
  "caption": [{"type": "text", "text": "Head office"}], "link": {"href": "https://example.com/office", "newTab": true},
  "size": "large", "width": "300px", "aspectRatio": "16/9", "align": "wide"
}`,
			want: Attrs{
				Media: "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", Alt: "Our office building", Decorative: true,
				Caption: headOffice, Link: &Link{Href: "https://example.com/office", NewTab: true},
				Size: "large", Width: "300px", AspectRatio: "16/9", Align: "wide",
			},
		},
		{name: "table", typ: BlockTable, in: `{"caption":[{"type":"text","text":"Head office"}]}`, want: Attrs{Caption: headOffice}},
		{name: "tableRow", typ: BlockTableRow, in: `{"section":"head"}`, want: Attrs{Section: "head"}},
		{
			name: "tableCell",
			typ:  BlockTableCell,
			in:   `{"header":true,"colspan":1000,"rowspan":1000,"textAlign":"left"}`,
			want: Attrs{Header: true, Colspan: 1000, Rowspan: 1000, TextAlign: "left"},
		},
		{name: "buttons", typ: BlockButtons, in: `{"justify":"spaceBetween"}`, want: Attrs{Justify: "spaceBetween"}},
		{
			name: "button",
			typ:  BlockButton,
			in:   `{"label":"Contact us","link":{"entry":"ent_01JB8Y6T2SA3F6H9J2K5M8N1P4","anchor":"form"},"width":"50%"}`,
			want: Attrs{Label: "Contact us", Link: &Link{Entry: "ent_01JB8Y6T2SA3F6H9J2K5M8N1P4", Anchor: "form"}, Width: "50%"},
		},
		{name: "spacer", typ: BlockSpacer, in: `{"height":{"value":"40px"}}`, want: Attrs{Height: Length{Value: "40px"}}},
		{name: "code", typ: BlockCode, in: `{"language":"go"}`, want: Attrs{Language: "go"}},
		{
			name: "embed",
			typ:  BlockEmbed,
			in:   `{"provider":"youtube","url":"https://youtu.be/dQw4w9WgXcQ","caption":[{"type":"text","text":"Head office"}]}`,
			want: Attrs{Provider: "youtube", URL: "https://youtu.be/dQw4w9WgXcQ", Caption: headOffice},
		},
		{
			name: "columns",
			typ:  BlockColumns,
			in:   `{"verticalAlign":"top","stackOnMobile":true}`,
			want: Attrs{VerticalAlign: "top", StackOnMobile: new(true)},
		},
		{
			name: "column",
			typ:  BlockColumn,
			in:   `{"width":"33.33%","verticalAlign":"stretch"}`,
			want: Attrs{Width: "33.33%", VerticalAlign: "stretch"},
		},
		{
			name: "group",
			typ:  BlockGroup,
			in: `{
  "layout": {"type": "row", "justify": "spaceBetween", "verticalAlign": "center", "nowrap": true, "columns": 16, "minColumnWidth": "12rem"},
  "tagName": "section", "label": "Notice", "align": "full"
}`,
			want: Attrs{
				Layout:  Layout{Type: "row", Justify: "spaceBetween", VerticalAlign: "center", NoWrap: true, Columns: 16, MinColumnWidth: "12rem"},
				TagName: "section", Label: "Notice", Align: "full",
			},
		},
		{
			name: "file",
			typ:  BlockFile,
			in:   `{"media":"med_01JB8Z3K5WN9C2E6H1K4P7S3T8","label":"Brochure (PDF)"}`,
			want: Attrs{Media: "med_01JB8Z3K5WN9C2E6H1K4P7S3T8", Label: "Brochure (PDF)"},
		},
		{name: "form", typ: BlockForm, in: `{"form":"frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"}`, want: Attrs{Form: "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"}},
		{name: "html", typ: BlockHTML, in: `{"html":"<ul>\n\t<li>a</li>\n</ul>"}`, want: Attrs{HTML: "<ul>\n\t<li>a</li>\n</ul>"}},
		{
			name: "legacy",
			typ:  BlockLegacy,
			in:   `{"html":"[gallery]","reason":"shortcode gallery"}`,
			want: Attrs{HTML: "[gallery]", Reason: "shortcode gallery"},
		},
		{name: "any type", typ: "", in: fullAttrsJSON, want: fullAttrs},

		{name: "ordered false", typ: BlockList, in: `{"ordered":false,"start":1}`, want: Attrs{Start: 1}},
		{name: "only false", typ: BlockList, in: `{"ordered":false}`, want: Attrs{}},
		{name: "decorative false", typ: BlockImage, in: `{"media":"med_01JB8Z3K5QH2V6T9C4R7Y1E8B5","decorative":false}`, want: Attrs{Media: "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"}},
		{name: "header false", typ: BlockTableCell, in: `{"header":false,"colspan":1,"rowspan":1}`, want: Attrs{Colspan: 1, Rowspan: 1}},
		{
			name: "nowrap false",
			typ:  BlockGroup,
			in:   `{"layout":{"type":"grid","nowrap":false,"columns":1}}`,
			want: Attrs{Layout: Layout{Type: "grid", Columns: 1}},
		},
		{name: "stackOnMobile false", typ: BlockColumns, in: `{"stackOnMobile":false}`, want: Attrs{StackOnMobile: new(false)}},
		{name: "empty caption", typ: BlockImage, in: `{"media":"med_01JB8Z3K5QH2V6T9C4R7Y1E8B5","caption":[]}`, want: Attrs{Media: "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"}},
		{name: "empty citation", typ: BlockQuote, in: `{"citation":[]}`, want: Attrs{}},
		{name: "lowest level", typ: BlockHeading, in: `{"level":2}`, want: Attrs{Level: 2}},
		{
			name: "reversed key order",
			typ:  BlockImage,
			in:   `{"align":"wide","size":"large","alt":"Our office building","media":"med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"}`,
			want: Attrs{Media: "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", Alt: "Our office building", Size: "large", Align: "wide"},
		},
		{name: "missing level not checked", typ: BlockHeading, in: `{"textAlign":"left"}`, want: Attrs{TextAlign: "left"}},
		{
			name: "values not checked",
			typ:  BlockImage,
			in:   `{"alt":"a\nb","link":{"href":"javascript:alert(1)"},"width":"-1px","align":"middle"}`,
			want: Attrs{Alt: "a\nb", Link: &Link{Href: "javascript:alert(1)"}, Width: "-1px", Align: "middle"},
		},
	}
	read := make(map[BlockType][]string) // the keys of the attributes read for each type
	for _, tt := range tests {
		read[tt.typ] = append(read[tt.typ], tt.want.keys()...)
		t.Run(tt.name, func(t *testing.T) {
			got := Attrs{Label: "old"}
			err := unmarshal([]byte(tt.in), "/attrs", func(d *decoder) error { return got.decodeFor(d, tt.typ) })
			if err != nil {
				t.Fatalf("decodeFor(%s, %q) = %v", tt.in, tt.typ, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeFor(%s, %q) = %#v, want %#v", tt.in, tt.typ, got, tt.want)
			}
			// UnmarshalJSON accepts the attributes of any type.
			got = Attrs{Label: "old"}
			if err := got.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnmarshalJSON(%s) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
	for typ, spec := range blockSpecs {
		for _, k := range spec.attrs {
			if !slices.Contains(read[typ], k) {
				t.Errorf("no test reads %s for %s", k, typ)
			}
		}
	}
}

func TestAttrsDecodeForError(t *testing.T) {
	tests := []struct {
		name string
		typ  BlockType
		in   string
		path string
		msg  string
	}{
		{name: "attrs of separator", typ: BlockSeparator, in: `{"level":2}`, path: "/attrs", msg: "not allowed for separator"},
		{name: "empty attrs of separator", typ: BlockSeparator, in: `{}`, path: "/attrs", msg: "not allowed for separator"},
		{name: "attrs of listItem", typ: BlockListItem, in: `{"section":"head"}`, path: "/attrs", msg: "not allowed for listItem"},
		{name: "level of paragraph", typ: BlockParagraph, in: `{"level":2}`, path: "/attrs/level", msg: "not allowed for paragraph"},
		{name: "invalid level of paragraph", typ: BlockParagraph, in: `{"level":"x"}`, path: "/attrs/level", msg: "not allowed for paragraph"},
		{
			name: "after an allowed key",
			typ:  BlockParagraph,
			in:   `{"textAlign":"left","level":2}`,
			path: "/attrs/level",
			msg:  "not allowed for paragraph",
		},
		{
			name: "html of image",
			typ:  BlockImage,
			in:   `{"media":"med_01JB8Z3K5QH2V6T9C4R7Y1E8B5","html":"<p>x</p>"}`,
			path: "/attrs/html",
			msg:  "not allowed for image",
		},
		{name: "unknown key", typ: BlockParagraph, in: `{"bogus":1}`, path: "/attrs/bogus", msg: "unknown key"},
		{name: "unknown key for any type", typ: "", in: `{"bogus":1}`, path: "/attrs/bogus", msg: "unknown key"},
		{name: "key case", typ: BlockHeading, in: `{"Level":2}`, path: "/attrs/Level", msg: "unknown key"},
		{name: "newTab of button", typ: BlockButton, in: `{"label":"a","newTab":true}`, path: "/attrs/newTab", msg: "unknown key"},
		{name: "empty object", typ: BlockParagraph, in: `{}`, path: "/attrs", msg: "empty object is not allowed"},
		{name: "null", typ: BlockParagraph, in: `null`, path: "/attrs", msg: "expected object, got null"},
		{name: "level 0", typ: BlockHeading, in: `{"level":0}`, path: "/attrs/level", msg: "level 0 is out of range (2-6)"},
		{name: "level 1", typ: BlockHeading, in: `{"level":1}`, path: "/attrs/level", msg: "level 1 is out of range (2-6)"},
		{name: "level 7", typ: BlockHeading, in: `{"level":7}`, path: "/attrs/level", msg: "level 7 is out of range (2-6)"},
		{name: "level 1 for any type", typ: "", in: `{"level":1}`, path: "/attrs/level", msg: "level 1 is out of range (2-6)"},
		{name: "level string", typ: BlockHeading, in: `{"level":"2"}`, path: "/attrs/level", msg: "expected number, got string"},
		{name: "level fraction", typ: BlockHeading, in: `{"level":2.0}`, path: "/attrs/level", msg: "must be an integer"},
		{name: "start 0", typ: BlockList, in: `{"ordered":true,"start":0}`, path: "/attrs/start", msg: "start 0 is out of range (1 or more)"},
		{name: "negative start", typ: BlockList, in: `{"start":-1}`, path: "/attrs/start", msg: "start -1 is out of range (1 or more)"},
		{name: "ordered string", typ: BlockList, in: `{"ordered":"true"}`, path: "/attrs/ordered", msg: "expected boolean, got string"},
		{name: "colspan 0", typ: BlockTableCell, in: `{"colspan":0}`, path: "/attrs/colspan", msg: "colspan 0 is out of range (1-1000)"},
		{name: "colspan 1001", typ: BlockTableCell, in: `{"colspan":1001}`, path: "/attrs/colspan", msg: "colspan 1001 is out of range (1-1000)"},
		{name: "rowspan 0", typ: BlockTableCell, in: `{"rowspan":0}`, path: "/attrs/rowspan", msg: "rowspan 0 is out of range (1-1000)"},
		{name: "rowspan 1001", typ: BlockTableCell, in: `{"rowspan":1001}`, path: "/attrs/rowspan", msg: "rowspan 1001 is out of range (1-1000)"},
		{
			name: "layout columns 0",
			typ:  BlockGroup,
			in:   `{"layout":{"type":"grid","columns":0}}`,
			path: "/attrs/layout/columns",
			msg:  "columns 0 is out of range (1-16)",
		},
		{
			name: "layout columns 17",
			typ:  BlockGroup,
			in:   `{"layout":{"type":"grid","columns":17}}`,
			path: "/attrs/layout/columns",
			msg:  "columns 17 is out of range (1-16)",
		},
		{
			name: "layout columns fraction",
			typ:  BlockGroup,
			in:   `{"layout":{"columns":1.5}}`,
			path: "/attrs/layout/columns",
			msg:  "must be an integer",
		},
		{name: "unknown layout key", typ: BlockGroup, in: `{"layout":{"type":"row","wrap":true}}`, path: "/attrs/layout/wrap", msg: "unknown key"},
		{name: "empty layout", typ: BlockGroup, in: `{"layout":{}}`, path: "/attrs/layout", msg: "empty object is not allowed"},
		{name: "string for layout", typ: BlockGroup, in: `{"layout":"row"}`, path: "/attrs/layout", msg: "expected object, got string"},
		{
			name: "string for nowrap",
			typ:  BlockGroup,
			in:   `{"layout":{"type":"row","nowrap":"true"}}`,
			path: "/attrs/layout/nowrap",
			msg:  "expected boolean, got string",
		},
		{
			name: "string for stackOnMobile",
			typ:  BlockColumns,
			in:   `{"stackOnMobile":"true"}`,
			path: "/attrs/stackOnMobile",
			msg:  "expected boolean, got string",
		},
		{
			name: "null stackOnMobile",
			typ:  BlockColumns,
			in:   `{"stackOnMobile":null}`,
			path: "/attrs/stackOnMobile",
			msg:  "expected boolean, got null",
		},
		{name: "object for caption", typ: BlockImage, in: `{"caption":{}}`, path: "/attrs/caption", msg: "expected array, got object"},
		{name: "invalid caption", typ: BlockTable, in: `{"caption":[{"type":"text"}]}`, path: "/attrs/caption/0", msg: `missing key "text"`},
		{name: "string for citation", typ: BlockQuote, in: `{"citation":"A customer"}`, path: "/attrs/citation", msg: "expected array, got string"},
		{name: "empty link", typ: BlockImage, in: `{"link":{}}`, path: "/attrs/link", msg: "empty object is not allowed"},
		{
			name: "unknown link key",
			typ:  BlockButton,
			in:   `{"link":{"url":"https://example.com"}}`,
			path: "/attrs/link/url",
			msg:  "unknown key",
		},
		{name: "empty height value", typ: BlockSpacer, in: `{"height":{"value":""}}`, path: "/attrs/height/value", msg: "empty string is not allowed"},
		{name: "string for height", typ: BlockSpacer, in: `{"height":"40px"}`, path: "/attrs/height", msg: "expected object, got string"},
		{name: "empty alt", typ: BlockImage, in: `{"alt":""}`, path: "/attrs/alt", msg: "empty string is not allowed"},
		{name: "duplicate alt", typ: BlockImage, in: `{"alt":"a","alt":"b"}`, path: "/attrs/alt", msg: "duplicate key"},
		{name: "number for html", typ: BlockHTML, in: `{"html":1}`, path: "/attrs/html", msg: "expected string, got number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := Attrs{Label: "old"}
			got := orig
			err := unmarshal([]byte(tt.in), "/attrs", func(d *decoder) error { return got.decodeFor(d, tt.typ) })
			assertError(t, err, tt.path, tt.msg)
			if !reflect.DeepEqual(got, orig) {
				t.Errorf("decodeFor(%s, %q) changed the value to %#v", tt.in, tt.typ, got)
			}
		})
	}
}

func TestJSONUnmarshalAttrsTypes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		in   string
		path string
	}{
		{name: "Attrs", v: new(Attrs), in: `{"level":2,"Alt":"a"}`, path: "/Alt"},
		{name: "Layout in Attrs", v: new(Attrs), in: `{"layout":{"Type":"row"}}`, path: "/layout/Type"},
		{name: "Layout", v: new(Layout), in: `{"type":"row","x":1}`, path: "/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, json.Unmarshal([]byte(tt.in), tt.v), tt.path, "unknown key")
		})
	}
}

// isSubsequence reports whether s is t with some elements removed.
func isSubsequence(s, t []string) bool {
	for _, x := range s {
		i := slices.Index(t, x)
		if i < 0 {
			return false
		}
		t = t[i+1:]
	}
	return true
}

func TestAttrsKeys(t *testing.T) {
	tests := []struct {
		name string
		in   Attrs
		want []string
	}{
		{name: "zero", in: Attrs{}, want: nil},
		{name: "empty rich texts", in: Attrs{Citation: RichText{}, Caption: RichText{}}, want: nil},
		{name: "stackOnMobile false", in: Attrs{StackOnMobile: new(false)}, want: []string{"stackOnMobile"}},
		{name: "empty link", in: Attrs{Link: &Link{}}, want: []string{"link"}},
		{name: "layout without type", in: Attrs{Layout: Layout{NoWrap: true}}, want: []string{"layout"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.keys(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keys() = %#v, want %#v", got, tt.want)
			}
		})
	}

	// The order in which Attrs writes the keys.
	order := []string{
		"level", "ordered", "start", "citation", "layout", "tagName", "provider", "url",
		"media", "alt", "decorative", "label", "caption", "link", "size", "width", "aspectRatio", "align",
		"verticalAlign", "stackOnMobile", "section", "header", "colspan", "rowspan",
		"textAlign", "justify", "height", "language", "form", "html", "reason",
	}
	typ := reflect.TypeFor[Attrs]()
	var names []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		names = append(names, name)
	}
	if !slices.Equal(names, order) {
		t.Fatalf("JSON names of the fields of Attrs = %q, want %q", names, order)
	}
	if got := fullAttrs.keys(); !slices.Equal(got, order) {
		t.Errorf("keys() with every attribute = %q, want %q", got, order)
	}
	full := reflect.ValueOf(fullAttrs)
	for i := range full.NumField() {
		var one Attrs
		reflect.ValueOf(&one).Elem().Field(i).Set(full.Field(i))
		if got := one.keys(); !slices.Equal(got, order[i:i+1]) {
			t.Errorf("keys() with only %s = %q, want %q", typ.Field(i).Name, got, order[i:i+1])
		}
	}

	union := make(map[string]bool)
	for bt, spec := range blockSpecs {
		for _, k := range spec.attrs {
			union[k] = true
		}
		if !isSubsequence(spec.attrs, order) {
			t.Errorf("attrs of %s = %q, want them in the order of %q", bt, spec.attrs, order)
		}
	}
	if got, want := slices.Sorted(maps.Keys(union)), slices.Sorted(slices.Values(order)); !slices.Equal(got, want) {
		t.Errorf("attrs of all block types = %q, want %q", got, want)
	}
}

func TestBlockSpecs(t *testing.T) {
	// The attrs are checked by TestAttrsKeys and TestAttrsDecodeFor.
	tests := []struct {
		typ  BlockType
		want blockSpec
	}{
		{typ: BlockParagraph, want: blockSpec{content: contentInline}},
		{typ: BlockHeading, want: blockSpec{content: contentInline}},
		{typ: BlockList, want: blockSpec{content: contentBlocks, child: BlockListItem, min: 1}},
		{typ: BlockListItem, want: blockSpec{content: contentBlocks, part: true, parent: BlockList, min: 1}},
		{typ: BlockQuote, want: blockSpec{content: contentBlocks, min: 1}},
		{typ: BlockImage, want: blockSpec{content: contentNone}},
		{typ: BlockTable, want: blockSpec{content: contentBlocks, child: BlockTableRow, min: 1}},
		{typ: BlockTableRow, want: blockSpec{content: contentBlocks, part: true, parent: BlockTable, child: BlockTableCell, min: 1}},
		{typ: BlockTableCell, want: blockSpec{content: contentInline, part: true, parent: BlockTableRow}},
		{typ: BlockButtons, want: blockSpec{content: contentBlocks, child: BlockButton, min: 1}},
		{typ: BlockButton, want: blockSpec{content: contentNone, parent: BlockButtons}},
		{typ: BlockSeparator, want: blockSpec{content: contentNone}},
		{typ: BlockSpacer, want: blockSpec{content: contentNone}},
		{typ: BlockCode, want: blockSpec{content: contentText}},
		{typ: BlockEmbed, want: blockSpec{content: contentNone}},
		{typ: BlockColumns, want: blockSpec{content: contentBlocks, child: BlockColumn, min: 1}},
		{typ: BlockColumn, want: blockSpec{content: contentBlocks, parent: BlockColumns}},
		{typ: BlockGroup, want: blockSpec{content: contentBlocks}},
		{typ: BlockFile, want: blockSpec{content: contentNone}},
		{typ: BlockForm, want: blockSpec{content: contentNone}},
		{typ: BlockHTML, want: blockSpec{content: contentNone}},
		{typ: BlockLegacy, want: blockSpec{content: contentNone}},
	}
	if len(blockSpecs) != len(tests) {
		t.Errorf("len(blockSpecs) = %d, want %d", len(blockSpecs), len(tests))
	}
	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			got, ok := blockSpecs[tt.typ]
			if !ok {
				t.Fatalf("blockSpecs has no %s", tt.typ)
			}
			got.attrs = nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("blockSpecs[%s] = %+v, want %+v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestBlockCheckAttrs(t *testing.T) {
	const (
		media   = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
		pdf     = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7"
		form    = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
		youTube = "https://youtu.be/dQw4w9WgXcQ"
	)
	text := RichText{{Type: InlineText, Text: "a"}}
	tab := RichText{{Type: InlineText, Text: "a\tb"}}
	para := Block{Type: BlockParagraph, ID: "b_0paragr1"}
	// Each returns blocks with a block of the type and attributes a, which has the parents and content that the type needs.
	block := func(typ BlockType, a Attrs) Blocks { return Blocks{{Type: typ, ID: "b_000block", Attrs: a}} }
	heading := func(a Attrs) Blocks { return Blocks{{Type: BlockHeading, ID: "b_0heading", Attrs: a, Text: text}} }
	list := func(a Attrs) Blocks {
		return Blocks{{Type: BlockList, ID: "b_00list01", Attrs: a, Children: Blocks{{Type: BlockListItem, Children: Blocks{para}}}}}
	}
	quote := func(a Attrs) Blocks {
		return Blocks{{Type: BlockQuote, ID: "b_000quote", Attrs: a, Children: Blocks{para}}}
	}
	table := func(a Attrs) Blocks {
		return Blocks{{Type: BlockTable, ID: "b_000table", Attrs: a, Children: Blocks{{Type: BlockTableRow, Children: Blocks{{Type: BlockTableCell}}}}}}
	}
	row := func(a Attrs) Blocks {
		return Blocks{{Type: BlockTable, ID: "b_000table", Children: Blocks{{Type: BlockTableRow, Attrs: a, Children: Blocks{{Type: BlockTableCell}}}}}}
	}
	cell := func(a Attrs) Blocks { return inTable(Block{Type: BlockTableCell, Attrs: a}) }
	buttons := func(a Attrs) Blocks {
		return Blocks{{Type: BlockButtons, ID: "b_0buttons", Attrs: a, Children: Blocks{{Type: BlockButton, ID: "b_0button1", Attrs: Attrs{Label: "Go"}}}}}
	}
	button := func(a Attrs) Blocks {
		return Blocks{{Type: BlockButtons, ID: "b_0buttons", Children: Blocks{{Type: BlockButton, ID: "b_0button1", Attrs: a}}}}
	}
	columns := func(a Attrs) Blocks {
		return Blocks{{Type: BlockColumns, ID: "b_0columns", Attrs: a, Children: Blocks{{Type: BlockColumn, ID: "b_0column1"}}}}
	}
	column := func(a Attrs) Blocks {
		return Blocks{{Type: BlockColumns, ID: "b_0columns", Children: Blocks{{Type: BlockColumn, ID: "b_0column1", Attrs: a}}}}
	}
	layout := func(l Layout) Blocks { return block(BlockGroup, Attrs{Layout: l}) }
	allowHTML := ValidateOptions{AllowHTML: true}
	imported := ValidateOptions{Import: true}
	existing := map[string]Ref{media: {Type: MediaImage}, pdf: {Type: MediaPDF}, form: {}}
	refs := ValidateOptions{Lookup: func(_ RefKind, id string) (Ref, bool) {
		r, ok := existing[id]
		return r, ok
	}}
	tests := []struct {
		name  string
		in    Blocks
		opts  ValidateOptions // html and legacy blocks need AllowHTML and Import
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "paragraph", in: block(BlockParagraph, Attrs{TextAlign: "left"})},
		{
			name:  "justified paragraph",
			in:    block(BlockParagraph, Attrs{TextAlign: "justify"}),
			paths: []string{"/0/attrs/textAlign"},
			msg:   `/0/attrs/textAlign: unknown textAlign "justify" (use left, center, right)`,
		},
		{
			name:  "attributes of other types not checked",
			in:    block(BlockParagraph, Attrs{Level: 7, Alt: "a\nb"}),
			paths: []string{"/0/attrs/level", "/0/attrs/alt"},
			msg:   "/0/attrs/level: not allowed for paragraph\n/0/attrs/alt: not allowed for paragraph",
		},

		{name: "lowest level", in: heading(Attrs{Level: 2, TextAlign: "center"})},
		{name: "highest level", in: heading(Attrs{Level: 6})},
		{name: "missing level", in: heading(Attrs{TextAlign: "right"}), paths: []string{"/0/attrs/level"}, msg: "/0/attrs/level: missing level"},
		{name: "level 1", in: heading(Attrs{Level: 1}), paths: []string{"/0/attrs/level"}, msg: "/0/attrs/level: level 1 is out of range (2-6)"},
		{name: "level 7", in: heading(Attrs{Level: 7}), paths: []string{"/0/attrs/level"}, msg: "/0/attrs/level: level 7 is out of range (2-6)"},
		{
			name:  "attributes of a heading in order",
			in:    heading(Attrs{Level: 7, TextAlign: "justify"}),
			paths: []string{"/0/attrs/level", "/0/attrs/textAlign"},
			msg:   `/0/attrs/textAlign: unknown textAlign "justify" (use left, center, right)`,
		},

		{name: "ordered list", in: list(Attrs{Ordered: true})},
		{name: "ordered list with start", in: list(Attrs{Ordered: true, Start: 3})},
		{name: "start without ordered", in: list(Attrs{Start: 3}), paths: []string{"/0/attrs/start"}, msg: "/0/attrs/start: start needs ordered"},
		{
			name:  "negative start",
			in:    list(Attrs{Ordered: true, Start: -1}),
			paths: []string{"/0/attrs/start"},
			msg:   "/0/attrs/start: start -1 is out of range (1 or more)",
		},
		{
			name:  "negative start without ordered",
			in:    list(Attrs{Start: -1}),
			paths: []string{"/0/attrs/start", "/0/attrs/start"},
			msg:   "/0/attrs/start: start needs ordered\n/0/attrs/start: start -1 is out of range (1 or more)",
		},

		{name: "quote with citation", in: quote(Attrs{Citation: text})},
		{
			name:  "tab in a citation",
			in:    quote(Attrs{Citation: tab}),
			paths: []string{"/0/attrs/citation/0/text"},
			msg:   "/0/attrs/citation/0/text: contains a line break or tab",
		},

		{
			name: "image",
			in: block(BlockImage, Attrs{
				Media: media, Alt: "Office", Caption: text, Link: &Link{Href: "https://example.com/office", NewTab: true},
				Size: "large", Width: "50%", AspectRatio: "1.5", Align: "full",
			}),
		},
		{name: "decorative image", in: block(BlockImage, Attrs{Media: media, Decorative: true})},
		{name: "missing media", in: block(BlockImage, Attrs{Alt: "Office"}), paths: []string{"/0/attrs/media"}, msg: "/0/attrs/media: missing media"},
		{
			name:  "invalid media",
			in:    block(BlockImage, Attrs{Media: "med 1", Alt: "Office"}),
			paths: []string{"/0/attrs/media"},
			msg:   `/0/attrs/media: invalid media ID "med 1"`,
		},
		{
			name:  "missing alt",
			in:    block(BlockImage, Attrs{Media: media}),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: missing alt; set decorative for a decorative image",
		},
		{
			name:  "alt of a decorative image",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Decorative: true}),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: alt cannot be combined with decorative",
		},
		{
			name:  "line break in alt",
			in:    block(BlockImage, Attrs{Media: media, Alt: "a\nb"}),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: contains a line break or tab",
		},
		{
			name:  "NFD in alt",
			in:    block(BlockImage, Attrs{Media: media, Alt: "cafe" + acute}),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: not in Unicode NFC; normalize it first",
		},
		{
			name:  "tab in a caption",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Caption: tab}),
			paths: []string{"/0/attrs/caption/0/text"},
			msg:   "/0/attrs/caption/0/text: contains a line break or tab",
		},
		{
			name:  "javascript link",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Link: &Link{Href: "javascript:alert(1)"}}),
			paths: []string{"/0/attrs/link/href"},
			msg:   `/0/attrs/link/href: scheme "javascript" is not allowed`,
		},
		{
			name:  "empty link of an image",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Link: &Link{}}),
			paths: []string{"/0/attrs/link"},
			msg:   "/0/attrs/link: link needs href, entry, media or anchor",
		},
		{
			name:  "invalid size",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Size: "extra large"}),
			paths: []string{"/0/attrs/size"},
			msg:   `/0/attrs/size: invalid name "extra large"`,
		},
		{
			name:  "negative width",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Width: "-1px"}),
			paths: []string{"/0/attrs/width"},
			msg:   `/0/attrs/width: negative length "-1px" is not allowed here`,
		},
		{
			name:  "calc width",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Width: "calc(100% - 1px)"}),
			paths: []string{"/0/attrs/width"},
			msg:   `/0/attrs/width: invalid length "calc(100% - 1px)"`,
		},
		{name: "ratio", in: block(BlockImage, Attrs{Media: media, Alt: "Office", AspectRatio: "16/9"})},
		{
			name:  "ratio with a zero",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", AspectRatio: "16/0"}),
			paths: []string{"/0/attrs/aspectRatio"},
			msg:   `/0/attrs/aspectRatio: invalid aspect ratio "16/0"`,
		},
		{
			name:  "ratio of letters",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", AspectRatio: "a/b"}),
			paths: []string{"/0/attrs/aspectRatio"},
			msg:   `/0/attrs/aspectRatio: invalid aspect ratio "a/b"`,
		},
		{
			name:  "zero ratio",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", AspectRatio: "0"}),
			paths: []string{"/0/attrs/aspectRatio"},
			msg:   `/0/attrs/aspectRatio: invalid aspect ratio "0"`,
		},
		{
			name:  "ratio with spaces",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", AspectRatio: "16 / 9"}),
			paths: []string{"/0/attrs/aspectRatio"},
			msg:   `/0/attrs/aspectRatio: invalid aspect ratio "16 / 9"`,
		},
		{
			name:  "unknown align",
			in:    block(BlockImage, Attrs{Media: media, Alt: "Office", Align: "middle"}),
			paths: []string{"/0/attrs/align"},
			msg:   `/0/attrs/align: unknown align "middle" (use left, center, right, wide, full)`,
		},
		{
			name: "attributes of an image in order",
			in: block(BlockImage, Attrs{
				Media: "a b", Alt: "a\tb", Decorative: true, Caption: tab, Link: &Link{},
				Size: "a b", Width: "1", AspectRatio: "0", Align: "top",
			}),
			paths: []string{
				"/0/attrs/media", "/0/attrs/alt", "/0/attrs/alt", "/0/attrs/caption/0/text", "/0/attrs/link",
				"/0/attrs/size", "/0/attrs/width", "/0/attrs/aspectRatio", "/0/attrs/align",
			},
			msg: "/0/attrs/alt: alt cannot be combined with decorative\n/0/attrs/alt: contains a line break or tab\n/0/attrs/caption/0/text",
		},

		{name: "table with caption", in: table(Attrs{Caption: text})},
		{
			name:  "tab in a table caption",
			in:    table(Attrs{Caption: tab}),
			paths: []string{"/0/attrs/caption/0/text"},
			msg:   "/0/attrs/caption/0/text: contains a line break or tab",
		},
		{name: "foot row", in: row(Attrs{Section: "foot"})},
		{
			name:  "unknown section",
			in:    row(Attrs{Section: "middle"}),
			paths: []string{"/0/content/0/attrs/section"},
			msg:   `/0/content/0/attrs/section: unknown section "middle" (use head, body, foot)`,
		},
		{name: "header cell", in: cell(Attrs{Header: true, Colspan: 1000, Rowspan: 1, TextAlign: "right"})},
		{
			name:  "colspan 1001",
			in:    cell(Attrs{Colspan: 1001}),
			paths: []string{"/0/content/0/content/0/attrs/colspan"},
			msg:   "/0/content/0/content/0/attrs/colspan: colspan 1001 is out of range (1-1000)",
		},
		{
			name:  "negative rowspan",
			in:    cell(Attrs{Rowspan: -1}),
			paths: []string{"/0/content/0/content/0/attrs/rowspan"},
			msg:   "/0/content/0/content/0/attrs/rowspan: rowspan -1 is out of range (1-1000)",
		},
		{
			name:  "justified cell",
			in:    cell(Attrs{TextAlign: "justify"}),
			paths: []string{"/0/content/0/content/0/attrs/textAlign"},
			msg:   `/0/content/0/content/0/attrs/textAlign: unknown textAlign "justify" (use left, center, right)`,
		},
		{
			name: "attributes of a cell in order",
			in:   cell(Attrs{Header: true, Colspan: 1001, Rowspan: 1001, TextAlign: "justify"}),
			paths: []string{
				"/0/content/0/content/0/attrs/colspan", "/0/content/0/content/0/attrs/rowspan", "/0/content/0/content/0/attrs/textAlign",
			},
		},

		{name: "buttons", in: buttons(Attrs{Justify: "spaceBetween"})},
		{
			name:  "stretched buttons",
			in:    buttons(Attrs{Justify: "stretch"}),
			paths: []string{"/0/attrs/justify"},
			msg:   `/0/attrs/justify: unknown justify "stretch" (use left, center, right, spaceBetween)`,
		},
		{name: "button", in: button(Attrs{Label: "Contact us", Link: &Link{Entry: "ent_01JB8Y6T2SA3F6H9J2K5M8N1P4"}, Width: "100%"})},
		{
			name:  "missing label",
			in:    button(Attrs{Width: "25%"}),
			paths: []string{"/0/content/0/attrs/label"},
			msg:   "/0/content/0/attrs/label: missing label",
		},
		{
			name:  "line break in a label",
			in:    button(Attrs{Label: "a\nb"}),
			paths: []string{"/0/content/0/attrs/label"},
			msg:   "/0/content/0/attrs/label: contains a line break or tab",
		},
		{
			name:  "empty link of a button",
			in:    button(Attrs{Label: "Go", Link: &Link{}}),
			paths: []string{"/0/content/0/attrs/link"},
			msg:   "/0/content/0/attrs/link: link needs href, entry, media or anchor",
		},
		{
			name:  "button width 30%",
			in:    button(Attrs{Label: "Go", Width: "30%"}),
			paths: []string{"/0/content/0/attrs/width"},
			msg:   `/0/content/0/attrs/width: unknown width "30%" (use 25%, 50%, 75%, 100%)`,
		},
		{
			name:  "attributes of a button in order",
			in:    button(Attrs{Label: "a\tb", Link: &Link{Href: "javascript:alert(1)"}, Width: "33%"}),
			paths: []string{"/0/content/0/attrs/label", "/0/content/0/attrs/link/href", "/0/content/0/attrs/width"},
			msg:   "/0/content/0/attrs/label: contains a line break or tab\n/0/content/0/attrs/link/href",
		},

		{name: "height preset", in: block(BlockSpacer, Attrs{Height: Length{Preset: "large"}})},
		{name: "height value", in: block(BlockSpacer, Attrs{Height: Length{Value: "40px"}})},
		{name: "missing height", in: block(BlockSpacer, Attrs{}), paths: []string{"/0/attrs/height"}, msg: "/0/attrs/height: missing height"},
		{
			name:  "percentage height",
			in:    block(BlockSpacer, Attrs{Height: Length{Value: "50%"}}),
			paths: []string{"/0/attrs/height/value"},
			msg:   `/0/attrs/height/value: percentage "50%" is not allowed here`,
		},
		{
			name:  "negative height",
			in:    block(BlockSpacer, Attrs{Height: Length{Value: "-1px"}}),
			paths: []string{"/0/attrs/height/value"},
			msg:   `/0/attrs/height/value: negative length "-1px" is not allowed here`,
		},
		{
			name:  "invalid height preset",
			in:    block(BlockSpacer, Attrs{Height: Length{Preset: "a b"}}),
			paths: []string{"/0/attrs/height/preset"},
			msg:   `/0/attrs/height/preset: invalid name "a b"`,
		},

		{name: "code", in: block(BlockCode, Attrs{Language: "go"})},
		{
			name:  "invalid language",
			in:    block(BlockCode, Attrs{Language: "c++"}),
			paths: []string{"/0/attrs/language"},
			msg:   `/0/attrs/language: invalid name "c++"`,
		},

		{name: "youtube", in: block(BlockEmbed, Attrs{Provider: "youtube", URL: youTube, Caption: text})},
		{name: "vimeo", in: block(BlockEmbed, Attrs{Provider: "vimeo", URL: "https://vimeo.com/76979871"})},
		{name: "x", in: block(BlockEmbed, Attrs{Provider: "x", URL: "https://x.com/jack/status/20"})},
		{name: "googleMaps", in: block(BlockEmbed, Attrs{Provider: "googleMaps", URL: "https://www.google.com/maps/embed?pb=!1m18"})},
		{
			name:  "missing provider",
			in:    block(BlockEmbed, Attrs{URL: "https://example.com/video"}),
			paths: []string{"/0/attrs/provider"},
			msg:   "/0/attrs/provider: missing provider",
		},
		{
			name:  "unknown provider",
			in:    block(BlockEmbed, Attrs{Provider: "instagram", URL: "https://www.instagram.com/p/C0abcDEFghi/"}),
			paths: []string{"/0/attrs/provider"},
			msg:   `/0/attrs/provider: unknown provider "instagram" (use youtube, vimeo, x, googleMaps)`,
		},
		{
			name:  "provider in another case",
			in:    block(BlockEmbed, Attrs{Provider: "YouTube", URL: youTube}),
			paths: []string{"/0/attrs/provider"},
			msg:   `/0/attrs/provider: unknown provider "YouTube"`,
		},
		{name: "missing url", in: block(BlockEmbed, Attrs{Provider: "youtube"}), paths: []string{"/0/attrs/url"}, msg: "/0/attrs/url: missing url"},
		{
			name:  "http url",
			in:    block(BlockEmbed, Attrs{Provider: "youtube", URL: "http://youtu.be/dQw4w9WgXcQ"}),
			paths: []string{"/0/attrs/url"},
			msg:   "/0/attrs/url: must be an https URL",
		},
		{
			name:  "url of another provider",
			in:    block(BlockEmbed, Attrs{Provider: "vimeo", URL: youTube}),
			paths: []string{"/0/attrs/url"},
			msg:   "/0/attrs/url: not a URL that vimeo can embed",
		},
		{
			name:  "credentials in url",
			in:    block(BlockEmbed, Attrs{Provider: "youtube", URL: "https://user:pass@youtu.be/dQw4w9WgXcQ"}),
			paths: []string{"/0/attrs/url"},
			msg:   "/0/attrs/url: credentials in URLs are not allowed",
		},
		{
			name:  "attributes of an embed in order",
			in:    block(BlockEmbed, Attrs{Provider: "instagram", URL: "javascript:alert(1)", Caption: tab}),
			paths: []string{"/0/attrs/provider", "/0/attrs/url", "/0/attrs/caption/0/text"},
			msg:   `/0/attrs/url: scheme "javascript" is not allowed`,
		},

		{name: "columns", in: columns(Attrs{VerticalAlign: "bottom", StackOnMobile: new(false)})},
		{
			name:  "stretched columns",
			in:    columns(Attrs{VerticalAlign: "stretch"}),
			paths: []string{"/0/attrs/verticalAlign"},
			msg:   `/0/attrs/verticalAlign: unknown verticalAlign "stretch" (use top, center, bottom)`,
		},
		{name: "column", in: column(Attrs{Width: "25%", VerticalAlign: "stretch"})},
		{
			name:  "negative column width",
			in:    column(Attrs{Width: "-10%"}),
			paths: []string{"/0/content/0/attrs/width"},
			msg:   `/0/content/0/attrs/width: negative length "-10%" is not allowed here`,
		},
		{
			name:  "unknown verticalAlign of a column",
			in:    column(Attrs{VerticalAlign: "middle"}),
			paths: []string{"/0/content/0/attrs/verticalAlign"},
			msg:   `/0/content/0/attrs/verticalAlign: unknown verticalAlign "middle" (use top, center, bottom, stretch)`,
		},
		{
			name:  "attributes of a column in order",
			in:    column(Attrs{Width: "calc(1px)", VerticalAlign: "middle"}),
			paths: []string{"/0/content/0/attrs/width", "/0/content/0/attrs/verticalAlign"},
		},

		{name: "group", in: block(BlockGroup, Attrs{TagName: "aside", Label: "Notice", Align: "wide"})},
		{name: "flow layout", in: layout(Layout{Type: "flow"})},
		{name: "row layout", in: layout(Layout{Type: "row", Justify: "left", VerticalAlign: "stretch", NoWrap: true})},
		{name: "stack layout", in: layout(Layout{Type: "stack", Justify: "stretch", VerticalAlign: "spaceBetween"})},
		{name: "grid layout", in: layout(Layout{Type: "grid", Columns: 16, MinColumnWidth: "0"})},
		{name: "layout with only nowrap false", in: mustBlocks(t, `[{"type":"group","id":"b_000block","attrs":{"layout":{"nowrap":false}}}]`)},
		{
			name:  "missing layout type",
			in:    layout(Layout{Justify: "bogus"}),
			paths: []string{"/0/attrs/layout/type"},
			msg:   "/0/attrs/layout/type: missing layout type",
		},
		{
			name:  "unknown layout type",
			in:    layout(Layout{Type: "flex", Justify: "bogus"}),
			paths: []string{"/0/attrs/layout/type"},
			msg:   `/0/attrs/layout/type: unknown type "flex" (use flow, row, stack, grid)`,
		},
		{
			name:  "justify of a grid",
			in:    layout(Layout{Type: "grid", Justify: "left"}),
			paths: []string{"/0/attrs/layout/justify"},
			msg:   "/0/attrs/layout/justify: not allowed for grid layout",
		},
		{
			name:  "columns of a row",
			in:    layout(Layout{Type: "row", Columns: 3}),
			paths: []string{"/0/attrs/layout/columns"},
			msg:   "/0/attrs/layout/columns: not allowed for row layout",
		},
		{
			name:  "nowrap of a stack",
			in:    layout(Layout{Type: "stack", NoWrap: true}),
			paths: []string{"/0/attrs/layout/nowrap"},
			msg:   "/0/attrs/layout/nowrap: not allowed for stack layout",
		},
		{
			name: "keys of a flow",
			in:   layout(Layout{Type: "flow", Justify: "bogus", VerticalAlign: "bogus", NoWrap: true, Columns: 17, MinColumnWidth: "bogus"}),
			paths: []string{
				"/0/attrs/layout/justify", "/0/attrs/layout/verticalAlign", "/0/attrs/layout/nowrap",
				"/0/attrs/layout/columns", "/0/attrs/layout/minColumnWidth",
			},
			msg: "/0/attrs/layout/justify: not allowed for flow layout\n/0/attrs/layout/verticalAlign: not allowed for flow layout\n" +
				"/0/attrs/layout/nowrap: not allowed for flow layout\n/0/attrs/layout/columns: not allowed for flow layout\n" +
				"/0/attrs/layout/minColumnWidth: not allowed for flow layout",
		},
		{
			name:  "stretched row",
			in:    layout(Layout{Type: "row", Justify: "stretch"}),
			paths: []string{"/0/attrs/layout/justify"},
			msg:   `/0/attrs/layout/justify: unknown justify "stretch" (use left, center, right, spaceBetween)`,
		},
		{
			name:  "row with verticalAlign spaceBetween",
			in:    layout(Layout{Type: "row", VerticalAlign: "spaceBetween"}),
			paths: []string{"/0/attrs/layout/verticalAlign"},
			msg:   `/0/attrs/layout/verticalAlign: unknown verticalAlign "spaceBetween" (use top, center, bottom, stretch)`,
		},
		{
			name:  "stack with justify spaceBetween",
			in:    layout(Layout{Type: "stack", Justify: "spaceBetween"}),
			paths: []string{"/0/attrs/layout/justify"},
			msg:   `/0/attrs/layout/justify: unknown justify "spaceBetween" (use left, center, right, stretch)`,
		},
		{
			name:  "stretched stack",
			in:    layout(Layout{Type: "stack", VerticalAlign: "stretch"}),
			paths: []string{"/0/attrs/layout/verticalAlign"},
			msg:   `/0/attrs/layout/verticalAlign: unknown verticalAlign "stretch" (use top, center, bottom, spaceBetween)`,
		},
		{
			name:  "17 grid columns",
			in:    layout(Layout{Type: "grid", Columns: 17}),
			paths: []string{"/0/attrs/layout/columns"},
			msg:   "/0/attrs/layout/columns: columns 17 is out of range (1-16)",
		},
		{
			name:  "percentage minColumnWidth",
			in:    layout(Layout{Type: "grid", MinColumnWidth: "50%"}),
			paths: []string{"/0/attrs/layout/minColumnWidth"},
			msg:   `/0/attrs/layout/minColumnWidth: percentage "50%" is not allowed here`,
		},
		{
			name:  "keys of a row in order",
			in:    layout(Layout{Type: "row", Justify: "bogus", VerticalAlign: "bogus", NoWrap: true}),
			paths: []string{"/0/attrs/layout/justify", "/0/attrs/layout/verticalAlign"},
		},
		{
			name:  "keys of a grid in order",
			in:    layout(Layout{Type: "grid", Columns: -1, MinColumnWidth: "-1px"}),
			paths: []string{"/0/attrs/layout/columns", "/0/attrs/layout/minColumnWidth"},
		},
		{
			name:  "main tagName",
			in:    block(BlockGroup, Attrs{TagName: "main"}),
			paths: []string{"/0/attrs/tagName"},
			msg:   `/0/attrs/tagName: unknown tagName "main" (use div, section, article, aside)`,
		},
		{
			name:  "tab in a group label",
			in:    block(BlockGroup, Attrs{Label: "a\tb"}),
			paths: []string{"/0/attrs/label"},
			msg:   "/0/attrs/label: contains a line break or tab",
		},
		{
			name:  "group aligned left",
			in:    block(BlockGroup, Attrs{Align: "left"}),
			paths: []string{"/0/attrs/align"},
			msg:   `/0/attrs/align: unknown align "left" (use wide, full)`,
		},
		{
			name:  "attributes of a group in order",
			in:    block(BlockGroup, Attrs{Layout: Layout{Type: "grid", Columns: 17}, TagName: "main", Label: "a\tb", Align: "left"}),
			paths: []string{"/0/attrs/layout/columns", "/0/attrs/tagName", "/0/attrs/label", "/0/attrs/align"},
			msg:   "/0/attrs/label: contains a line break or tab\n/0/attrs/align",
		},

		{name: "file", in: block(BlockFile, Attrs{Media: "med_01JB8Z3K5WN9C2E6H1K4P7S3T8", Label: "Brochure (PDF)"})},
		{name: "missing media of a file", in: block(BlockFile, Attrs{Label: "Brochure"}), paths: []string{"/0/attrs/media"}, msg: "/0/attrs/media: missing media"},
		{
			name:  "attributes of a file in order",
			in:    Blocks{{Type: BlockFile, ID: "b_000block", Attrs: Attrs{Media: "med/1", Label: "a\nb"}, Code: "x"}},
			paths: []string{"/0/attrs/media", "/0/attrs/label", "/0/content"},
			msg:   "/0/attrs/label: contains a line break or tab\n/0/content: text content is not allowed for file",
		},
		{name: "form", in: block(BlockForm, Attrs{Form: form})},
		{name: "missing form", in: block(BlockForm, Attrs{}), paths: []string{"/0/attrs/form"}, msg: "/0/attrs/form: missing form"},
		{
			name:  "invalid form",
			in:    block(BlockForm, Attrs{Form: "a b"}),
			paths: []string{"/0/attrs/form"},
			msg:   `/0/attrs/form: invalid form ID "a b"`,
		},

		{name: "existing media of an image", in: block(BlockImage, Attrs{Media: media, Alt: "Office"}), opts: refs},
		{name: "existing media of a file", in: block(BlockFile, Attrs{Media: media}), opts: refs},
		{name: "PDF of a file", in: block(BlockFile, Attrs{Media: pdf}), opts: refs},
		{
			name:  "PDF of an image",
			in:    block(BlockImage, Attrs{Media: pdf, Alt: "Office"}),
			opts:  refs,
			paths: []string{"/0/attrs/media"},
			msg:   `/0/attrs/media: media "` + pdf + `" (pdf) is not accepted here (accept: image)`,
		},
		{name: "existing form", in: block(BlockForm, Attrs{Form: form}), opts: refs},
		{
			name:  "media of an image that does not exist",
			in:    block(BlockImage, Attrs{Media: "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9", Alt: "Office"}),
			opts:  refs,
			paths: []string{"/0/attrs/media"},
			msg:   `/0/attrs/media: media "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9" does not exist`,
		},
		{
			name:  "media of a file that does not exist",
			in:    block(BlockFile, Attrs{Media: "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9"}),
			opts:  refs,
			paths: []string{"/0/attrs/media"},
			msg:   `/0/attrs/media: media "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9" does not exist`,
		},
		{
			name:  "form that does not exist",
			in:    block(BlockForm, Attrs{Form: "frm_01JB8Z3K5WQ3E6H9K1N4R7V2X5"}),
			opts:  refs,
			paths: []string{"/0/attrs/form"},
			msg:   `/0/attrs/form: form "frm_01JB8Z3K5WQ3E6H9K1N4R7V2X5" does not exist`,
		},
		{
			name:  "invalid media not looked up",
			in:    block(BlockImage, Attrs{Media: "a b", Alt: "Office"}),
			opts:  refs,
			paths: []string{"/0/attrs/media"},
			msg:   `/0/attrs/media: invalid media ID "a b"`,
		},

		{name: "html", in: block(BlockHTML, Attrs{HTML: "<p>cafe" + acute + "</p>\n\t<br>"}), opts: allowHTML},
		{name: "missing html", in: block(BlockHTML, Attrs{}), opts: allowHTML, paths: []string{"/0/attrs/html"}, msg: "/0/attrs/html: missing html"},
		{
			name:  "control character in html",
			in:    block(BlockHTML, Attrs{HTML: "<p>a\x00b</p>"}),
			opts:  allowHTML,
			paths: []string{"/0/attrs/html"},
			msg:   "/0/attrs/html: contains control characters; normalize it first",
		},
		{name: "legacy", in: block(BlockLegacy, Attrs{HTML: "<p>cafe" + acute + "</p>\n\t[gallery]", Reason: "shortcode gallery"}), opts: imported},
		{name: "legacy without reason", in: block(BlockLegacy, Attrs{HTML: "[gallery]"}), opts: imported},
		{
			name:  "missing html of a legacy block",
			in:    block(BlockLegacy, Attrs{Reason: "shortcode gallery"}),
			opts:  imported,
			paths: []string{"/0/attrs/html"},
			msg:   "/0/attrs/html: missing html",
		},
		{
			name:  "control character in legacy html",
			in:    block(BlockLegacy, Attrs{HTML: "a\rb"}),
			opts:  imported,
			paths: []string{"/0/attrs/html"},
			msg:   "/0/attrs/html: contains control characters; normalize it first",
		},
		{
			name:  "line break in a reason",
			in:    block(BlockLegacy, Attrs{HTML: "[gallery]", Reason: "a\nb"}),
			opts:  imported,
			paths: []string{"/0/attrs/reason"},
			msg:   "/0/attrs/reason: contains a line break or tab",
		},
		{
			name:  "attributes of a legacy block in order",
			in:    Blocks{{Type: BlockLegacy, ID: "b_000block", Attrs: Attrs{HTML: "a\x00b", Reason: "a\tb"}, Code: "x"}},
			opts:  imported,
			paths: []string{"/0/attrs/html", "/0/attrs/reason", "/0/content"},
			msg:   "/0/attrs/reason: contains a line break or tab\n/0/content: text content is not allowed for legacy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocksValidate(t, tt.in, tt.opts, tt.paths, tt.msg)
		})
	}
}

func TestCheckAspectRatio(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{name: "ratio", in: "16/9", ok: true},
		{name: "number", in: "1.5", ok: true},
		{name: "integer", in: "1", ok: true},
		{name: "fractions", in: ".5/1.25", ok: true},

		{name: "empty", in: ""},
		{name: "zero", in: "0"},
		{name: "zero width", in: "0/9"},
		{name: "zero height", in: "16/0"},
		{name: "letters", in: "a/b"},
		{name: "spaces", in: "16 / 9"},
		{name: "three numbers", in: "16/9/1"},
		{name: "missing width", in: "/9"},
		{name: "missing height", in: "16/"},
		{name: "slash", in: "/"},
		{name: "negative", in: "-16/9"},
		{name: "colon", in: "16:9"},
		{name: "exponent", in: "1e2"},
		{name: "auto", in: "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ""
			if !tt.ok {
				msg = fmt.Sprintf("invalid aspect ratio %q", tt.in)
			}
			assertErrorMsg(t, checkAspectRatio(tt.in), msg)
		})
	}
}

func TestEmbedProviderNames(t *testing.T) {
	got := slices.Sorted(slices.Values(embedProviderNames))
	if want := slices.Sorted(maps.Keys(embedProviders)); !slices.Equal(got, want) {
		t.Errorf("embedProviderNames = %q, want the keys of embedProviders %q", embedProviderNames, want)
	}
}
