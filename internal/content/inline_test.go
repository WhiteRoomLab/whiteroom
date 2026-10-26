package content

import (
	"encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// conceptRichTextJSON is the example in CONCEPT.md 6.5.2.
const conceptRichTextJSON = `[
  { "type": "text", "text": "当社の" },
  { "type": "text", "text": "事業内容", "marks": [{ "type": "bold" }] },
  { "type": "text", "text": "をご紹介します。" },
  { "type": "hardBreak" },
  { "type": "text", "text": "サービス一覧", "marks": [{ "type": "link", "attrs": { "entry": "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" } }] }
]`

// fullRichTextJSON has every mark and every kind of link and highlight, in canonical form.
const fullRichTextJSON = `[
  {"type": "text", "text": "plain"},
  {"type": "text", "text": "styled", "marks": [
    {"type": "bold"}, {"type": "italic"}, {"type": "underline"}, {"type": "strike"}, {"type": "code"}, {"type": "subscript"}
  ]},
  {"type": "text", "text": "2", "marks": [{"type": "superscript"}, {"type": "highlight", "attrs": {"variant": "marker"}}]},
  {"type": "hardBreak"},
  {"type": "text", "text": "colored", "marks": [
    {"type": "highlight", "attrs": {"color": {"preset": "accent"}, "backgroundColor": {"value": "#fff4e5"}}}
  ]},
  {"type": "text", "text": "entry", "marks": [
    {"type": "link", "attrs": {"entry": "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3", "anchor": "contact", "newTab": true, "rel": ["nofollow", "sponsored", "ugc"]}}
  ]},
  {"type": "text", "text": "media", "marks": [{"type": "link", "attrs": {"media": "med_01JB8Y6T2SB7C1D4E8F2G5H9J3"}}]},
  {"type": "text", "text": "mail", "marks": [{"type": "link", "attrs": {"href": "mailto:info@example.com"}}]},
  {"type": "text", "text": "anchor", "marks": [{"type": "link", "attrs": {"anchor": "contact"}}]}
]`

func TestRichTextUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want RichText
	}{
		{
			name: "concept example",
			in:   conceptRichTextJSON,
			want: RichText{
				{Type: InlineText, Text: "当社の"},
				{Type: InlineText, Text: "事業内容", Marks: []Mark{{Type: MarkBold}}},
				{Type: InlineText, Text: "をご紹介します。"},
				{Type: InlineHardBreak},
				{Type: InlineText, Text: "サービス一覧", Marks: []Mark{{Type: MarkLink, Link: &Link{Entry: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"}}}},
			},
		},
		{
			name: "every mark",
			in:   fullRichTextJSON,
			want: RichText{
				{Type: InlineText, Text: "plain"},
				{Type: InlineText, Text: "styled", Marks: []Mark{
					{Type: MarkBold}, {Type: MarkItalic}, {Type: MarkUnderline}, {Type: MarkStrike}, {Type: MarkCode}, {Type: MarkSubscript},
				}},
				{Type: InlineText, Text: "2", Marks: []Mark{
					{Type: MarkSuperscript}, {Type: MarkHighlight, Highlight: &Highlight{Variant: "marker"}},
				}},
				{Type: InlineHardBreak},
				{Type: InlineText, Text: "colored", Marks: []Mark{
					{Type: MarkHighlight, Highlight: &Highlight{Color: Color{Preset: "accent"}, BackgroundColor: Color{Value: "#fff4e5"}}},
				}},
				{Type: InlineText, Text: "entry", Marks: []Mark{{Type: MarkLink, Link: &Link{
					Entry: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3", Anchor: "contact", NewTab: true, Rel: []Rel{RelNofollow, RelSponsored, RelUGC},
				}}}},
				{Type: InlineText, Text: "media", Marks: []Mark{{Type: MarkLink, Link: &Link{Media: "med_01JB8Y6T2SB7C1D4E8F2G5H9J3"}}}},
				{Type: InlineText, Text: "mail", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: "mailto:info@example.com"}}}},
				{Type: InlineText, Text: "anchor", Marks: []Mark{{Type: MarkLink, Link: &Link{Anchor: "contact"}}}},
			},
		},
		{name: "empty", in: `[]`, want: nil},
		{
			name: "attrs before type",
			in:   `[{"type":"text","text":"a","marks":[{"attrs":{"href":"https://example.com"},"type":"link"}]}]`,
			want: RichText{{Type: InlineText, Text: "a", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: "https://example.com"}}}}},
		},
		{
			name: "newTab false",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com","newTab":false}}]}]`,
			want: RichText{{Type: InlineText, Text: "a", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: "https://example.com"}}}}},
		},
		{
			name: "javascript URL not checked",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]`,
			want: RichText{{Type: InlineText, Text: "a", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: "javascript:alert(1)"}}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RichText{{Type: InlineText, Text: "old"}}
			if err := got.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnmarshalJSON(%s) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRichTextUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "null", in: `null`, path: "", msg: "expected array, got null"},
		{name: "null node", in: `[null]`, path: "/0", msg: "expected object, got null"},
		{name: "empty node", in: `[{}]`, path: "/0", msg: "empty object is not allowed"},
		{name: "missing type", in: `[{"text":"a"}]`, path: "/0", msg: `missing key "type"`},
		{name: "type case", in: `[{"type":"Text","text":"a"}]`, path: "/0/type", msg: `unknown inline type "Text"`},
		{name: "ruby", in: `[{"type":"ruby","text":"a"}]`, path: "/0/type", msg: `unknown inline type "ruby"`},
		{name: "missing text", in: `[{"type":"text"}]`, path: "/0", msg: `missing key "text"`},
		{name: "missing text in second node", in: `[{"type":"hardBreak"},{"type":"text"}]`, path: "/1", msg: `missing key "text"`},
		{name: "empty text", in: `[{"type":"text","text":""}]`, path: "/0/text", msg: "empty string is not allowed"},
		{name: "key case", in: `[{"type":"text","Text":"a"}]`, path: "/0/Text", msg: "unknown key"},
		{name: "duplicate text", in: `[{"type":"text","text":"a","text":"b"}]`, path: "/0/text", msg: "duplicate key"},
		{name: "empty marks", in: `[{"type":"text","text":"a","marks":[]}]`, path: "/0/marks", msg: "empty array is not allowed"},
		{name: "hardBreak with text", in: `[{"type":"hardBreak","text":"a"}]`, path: "/0/text", msg: "not allowed for hardBreak"},
		{
			name: "hardBreak with marks",
			in:   `[{"type":"hardBreak","marks":[{"type":"bold"}]}]`,
			path: "/0/marks",
			msg:  "not allowed for hardBreak",
		},
		{
			name: "unknown mark",
			in:   `[{"type":"text","text":"a","marks":[{"type":"blink"}]}]`,
			path: "/0/marks/0/type",
			msg:  `unknown mark type "blink"`,
		},
		{
			name: "fontSize mark",
			in:   `[{"type":"text","text":"a","marks":[{"type":"fontSize"}]}]`,
			path: "/0/marks/0/type",
			msg:  `unknown mark type "fontSize"`,
		},
		{
			name: "mark type case",
			in:   `[{"type":"text","text":"a","marks":[{"type":"Bold"}]}]`,
			path: "/0/marks/0/type",
			msg:  `unknown mark type "Bold"`,
		},
		{
			name: "mark key case",
			in:   `[{"type":"text","text":"a","marks":[{"Type":"bold"}]}]`,
			path: "/0/marks/0/Type",
			msg:  "unknown key",
		},
		{
			name: "missing mark type",
			in:   `[{"type":"text","text":"a","marks":[{"attrs":{"href":"https://example.com"}}]}]`,
			path: "/0/marks/0",
			msg:  `missing key "type"`,
		},
		{
			name: "bold with attrs",
			in:   `[{"type":"text","text":"a","marks":[{"type":"bold","attrs":{"href":"https://example.com"}}]}]`,
			path: "/0/marks/0/attrs",
			msg:  "not allowed for bold",
		},
		{
			name: "link without attrs",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link"}]}]`,
			path: "/0/marks/0",
			msg:  `missing key "attrs"`,
		},
		{
			name: "highlight without attrs",
			in:   `[{"type":"text","text":"a","marks":[{"type":"highlight"}]}]`,
			path: "/0/marks/0",
			msg:  `missing key "attrs"`,
		},
		{
			name: "null attrs",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":null}]}]`,
			path: "/0/marks/0/attrs",
			msg:  "expected object, got null",
		},
		{
			name: "empty attrs",
			in:   `[{"type":"text","text":"a","marks":[{"type":"highlight","attrs":{}}]}]`,
			path: "/0/marks/0/attrs",
			msg:  "empty object is not allowed",
		},
		{
			name: "unknown link attribute",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"url":"https://example.com"}}]}]`,
			path: "/0/marks/0/attrs/url",
			msg:  "unknown key",
		},
		{
			name: "unknown highlight attribute",
			in:   `[{"type":"text","text":"a","marks":[{"type":"highlight","attrs":{"textColor":{"value":"#f00"}}}]}]`,
			path: "/0/marks/0/attrs/textColor",
			msg:  "unknown key",
		},
		{
			name: "empty rel",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com","rel":[]}}]}]`,
			path: "/0/marks/0/attrs/rel",
			msg:  "empty array is not allowed",
		},
		{
			name: "number in rel",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com","rel":["nofollow",1]}}]}]`,
			path: "/0/marks/0/attrs/rel/1",
			msg:  "expected string, got number",
		},
		{
			name: "string for newTab",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com","newTab":"true"}}]}]`,
			path: "/0/marks/0/attrs/newTab",
			msg:  "expected boolean, got string",
		},
		{
			name: "string for highlight color",
			in:   `[{"type":"text","text":"a","marks":[{"type":"highlight","attrs":{"color":"#f00"}}]}]`,
			path: "/0/marks/0/attrs/color",
			msg:  "expected object, got string",
		},
		{name: "lone surrogate in text", in: `[{"type":"text","text":"\ud800"}]`, path: "/0/text", msg: "surrogate"},
		{name: "invalid UTF-8 in text", in: `[{"type":"text","text":"` + "\xff" + `"}]`, path: "/0/text", msg: "invalid UTF-8"},
		{
			name: "lone surrogate in href",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"\ud800"}}]}]`,
			path: "/0/marks/0/attrs/href",
			msg:  "surrogate",
		},
		{
			name: "duplicate href",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com","href":"/"}}]}]`,
			path: "/0/marks/0/attrs/href",
			msg:  "duplicate key",
		},
		{name: "trailing data", in: `[] []`, path: "", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := RichText{{Type: InlineText, Text: "old"}}
			rt := slices.Clone(orig)
			assertError(t, rt.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(rt, orig) {
				t.Errorf("UnmarshalJSON(%s) changed the value to %#v", tt.in, rt)
			}
		})
	}
}

func TestRichTextUnmarshalJSONRawPointer(t *testing.T) {
	// attrs before type is read as a raw value; the duplicate key falls at every offset of the decoder's buffer.
	for n := 1; n < 200; n++ {
		in := `[{"type":"text","text":"` + strings.Repeat("p", n) +
			`","marks":[{"attrs":{"color":{"value":"#fff","value":"#000"}},"type":"highlight"}]}]`
		var rt RichText
		assertError(t, rt.UnmarshalJSON([]byte(in)), "/0/marks/0/attrs/color/value", "duplicate key")
	}
}

func TestJSONUnmarshalInlineTypes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		in   string
		path string
	}{
		{name: "RichText", v: new(RichText), in: `[{"type":"text","text":"a","x":1}]`, path: "/0/x"},
		{name: "Inline", v: new(Inline), in: `{"type":"text","Text":"a"}`, path: "/Text"},
		{name: "Mark", v: new(Mark), in: `{"type":"link","attrs":{"url":"https://example.com"}}`, path: "/attrs/url"},
		{name: "Highlight", v: new(Highlight), in: `{"Variant":"marker"}`, path: "/Variant"},
		{name: "Link", v: new(Link), in: `{"url":"https://example.com"}`, path: "/url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, json.Unmarshal([]byte(tt.in), tt.v), tt.path, "unknown key")
		})
	}
}

func TestJSONMarshalInlineTypes(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "nil rich text", in: RichText(nil), want: `[]`},
		{name: "empty rich text", in: RichText{}, want: `[]`},
		{name: "hardBreak", in: Inline{Type: InlineHardBreak}, want: `{"type":"hardBreak"}`},
		{name: "empty marks", in: Inline{Type: InlineText, Text: "a", Marks: []Mark{}}, want: `{"type":"text","text":"a"}`},
		{name: "bold", in: Mark{Type: MarkBold}, want: `{"type":"bold"}`},
		{name: "bold with link attrs", in: Mark{Type: MarkBold, Link: &Link{Href: "https://example.com"}}, want: `{"type":"bold"}`},
		{name: "link without attrs", in: Mark{Type: MarkLink}, want: `{"type":"link"}`},
		{name: "link with highlight attrs", in: Mark{Type: MarkLink, Highlight: &Highlight{Variant: "marker"}}, want: `{"type":"link"}`},
		{name: "highlight without attrs", in: Mark{Type: MarkHighlight}, want: `{"type":"highlight"}`},
		{name: "highlight with link attrs", in: Mark{Type: MarkHighlight, Link: &Link{Anchor: "top"}}, want: `{"type":"highlight"}`},
		{name: "unknown mark", in: Mark{Type: "blink", Link: &Link{Anchor: "top"}}, want: `{"type":"blink"}`},
		{
			name: "false and empty attrs",
			in:   Mark{Type: MarkLink, Link: &Link{Anchor: "top", NewTab: false, Rel: []Rel{}}},
			want: `{"type":"link","attrs":{"anchor":"top"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal(%#v) = %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal(%#v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestRichTextRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "concept example", in: conceptRichTextJSON},
		{name: "every mark", in: fullRichTextJSON},
		{name: "empty", in: `[]`},
		{name: "quote and backslash", in: `[{"type":"text","text":"say \"hi\" \\ bye"}]`},
		{
			name: "reversed key order",
			in: `[{"marks": [
  {"attrs": {"rel": ["ugc"], "newTab": true, "anchor": "contact", "entry": "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"}, "type": "link"},
  {"attrs": {"backgroundColor": {"value": "#fff4e5"}, "color": {"preset": "accent"}}, "type": "highlight"}
], "text": "a", "type": "text"}]`,
			want: `[{"type": "text", "text": "a", "marks": [
  {"type": "link", "attrs": {"entry": "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3", "anchor": "contact", "newTab": true, "rel": ["ugc"]}},
  {"type": "highlight", "attrs": {"color": {"preset": "accent"}, "backgroundColor": {"value": "#fff4e5"}}}
]}]`,
		},
		{
			name: "newTab false",
			in:   `[{"type":"text","text":"a","marks":[{"attrs":{"newTab":false,"href":"https://example.com"},"type":"link"}]}]`,
			want: `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}]`,
		},
		{name: "escaped text", in: `[{"type":"text","text":"\u3042"}]`, want: `[{"type":"text","text":"あ"}]`},
		{
			name: "escaped key in attrs",
			in:   `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"hr\u0065f":"https://example.com"}}]}]`,
			want: `[{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"https://example.com"}}]}]`,
		},
		{name: "spaces in empty array", in: `[ ]`, want: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[RichText](t, tt.in, want)
		})
	}
}

// assertRichTextValidate checks the paths of the problems that Validate finds in rt and, if msg is not "", their messages.
// It also checks that rt.check(c, "/content") reports the same paths under "/content".
func assertRichTextValidate(t *testing.T, rt RichText, paths []string, msg string) {
	t.Helper()
	err := rt.Validate()
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
	rt.check(&c, "/content")
	var want []string
	for _, p := range paths {
		want = append(want, "/content"+p)
	}
	if got := errorPaths(t, c.result()); !reflect.DeepEqual(got, want) {
		t.Errorf(`check(c, "/content") paths = %q, want %q`, got, want)
	}
}

// markedText is a rich text of one text "a" with marks.
func markedText(marks ...Mark) RichText {
	return RichText{{Type: InlineText, Text: "a", Marks: marks}}
}

func TestRichTextNormalize(t *testing.T) {
	tests := []struct {
		name  string
		in    RichText
		want  RichText
		paths []string // problems that Validate finds after Normalize
	}{
		{name: "nil", in: nil, want: nil},
		{name: "empty", in: RichText{}, want: nil},
		{name: "NFD", in: RichText{{Type: InlineText, Text: "e" + acute}}, want: RichText{{Type: InlineText, Text: eAcute}}},
		{
			name: "NFD split across texts",
			in:   RichText{{Type: InlineText, Text: "e", Marks: []Mark{{Type: MarkBold}}}, {Type: InlineText, Text: acute}},
			want: RichText{{Type: InlineText, Text: "e", Marks: []Mark{{Type: MarkBold}}}, {Type: InlineText, Text: acute}},
		},
		{name: "NUL", in: RichText{{Type: InlineText, Text: "a\x00b"}}, want: RichText{{Type: InlineText, Text: "ab"}}},
		{name: "invalid UTF-8", in: RichText{{Type: InlineText, Text: "a\xffb"}}, want: RichText{{Type: InlineText, Text: "a\ufffdb"}}},
		{
			name: "text left empty",
			in: RichText{
				{Type: InlineText, Text: "a"},
				{Type: InlineText, Text: "\x00", Marks: []Mark{{Type: MarkBold}}},
				{Type: InlineText, Text: "b"},
			},
			want: RichText{{Type: InlineText, Text: "a"}, {Type: InlineText, Text: "b"}},
		},
		{name: "every text left empty", in: RichText{{Type: InlineText, Text: "\x00\x7f"}, {Type: InlineText}}, want: nil},
		{
			name:  "tab",
			in:    RichText{{Type: InlineText, Text: "a\tb"}},
			want:  RichText{{Type: InlineText, Text: "a\tb"}},
			paths: []string{"/0/text"},
		},
		{
			name: "hardBreak",
			in:   RichText{{Type: InlineText, Text: "a\x00"}, {Type: InlineHardBreak}, {Type: InlineText, Text: "b"}},
			want: RichText{{Type: InlineText, Text: "a"}, {Type: InlineHardBreak}, {Type: InlineText, Text: "b"}},
		},
		{
			name:  "hardBreak with text and marks",
			in:    RichText{{Type: InlineHardBreak, Text: "\x00", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: " /a "}}}}},
			want:  RichText{{Type: InlineHardBreak, Text: "\x00", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: " /a "}}}}},
			paths: []string{"/0/text", "/0/marks"},
		},
		{
			name: "surrounding spaces in href",
			in:   markedText(Mark{Type: MarkLink, Link: &Link{Href: " https://example.com\n"}}),
			want: markedText(Mark{Type: MarkLink, Link: &Link{Href: "https://example.com"}}),
		},
		{
			name:  "tab in scheme",
			in:    markedText(Mark{Type: MarkLink, Link: &Link{Href: "java\tscript:alert(1)"}}),
			want:  markedText(Mark{Type: MarkLink, Link: &Link{Href: "javascript:alert(1)"}}),
			paths: []string{"/0/marks/0/attrs/href"},
		},
		{
			name:  "anchor with space",
			in:    markedText(Mark{Type: MarkLink, Link: &Link{Anchor: " top"}}),
			want:  markedText(Mark{Type: MarkLink, Link: &Link{Anchor: " top"}}),
			paths: []string{"/0/marks/0/attrs/anchor"},
		},
		{
			name:  "color with space",
			in:    markedText(Mark{Type: MarkHighlight, Highlight: &Highlight{Color: Color{Value: " #fff"}}}),
			want:  markedText(Mark{Type: MarkHighlight, Highlight: &Highlight{Color: Color{Value: " #fff"}}}),
			paths: []string{"/0/marks/0/attrs/color/value"},
		},
		{
			name:  "link without attrs",
			in:    markedText(Mark{Type: MarkLink}),
			want:  markedText(Mark{Type: MarkLink}),
			paths: []string{"/0/marks/0/attrs"},
		},
		{
			name:  "unknown mark",
			in:    markedText(Mark{Type: "blink", Link: &Link{Href: " /a "}}),
			want:  markedText(Mark{Type: "blink", Link: &Link{Href: " /a "}}),
			paths: []string{"/0/marks/0/type"},
		},
		{
			name:  "unknown node",
			in:    RichText{{Type: "ruby", Text: "\x00"}},
			want:  RichText{{Type: "ruby", Text: "\x00"}},
			paths: []string{"/0/type"},
		},
		{
			name: "text and href valid after Normalize",
			in:   RichText{{Type: InlineText, Text: "a\x00b", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: " https://example.com "}}}}},
			want: RichText{{Type: InlineText, Text: "ab", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: "https://example.com"}}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Normalize() = %#v, want %#v", got, tt.want)
			}
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize() again = %#v, want %#v", got, tt.want)
			}
			assertRichTextValidate(t, got, tt.paths, "")
		})
	}
}

// linkJSON is a rich text of one text with a link mark that has attrs.
func linkJSON(attrs string) string {
	return `[{"type":"text","text":"a","marks":[{"type":"link","attrs":` + attrs + `}]}]`
}

// highlightJSON is a rich text of one text with a highlight mark that has attrs.
func highlightJSON(attrs string) string {
	return `[{"type":"text","text":"a","marks":[{"type":"highlight","attrs":` + attrs + `}]}]`
}

func TestRichTextValidate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		paths []string
		msg   string // part of the messages; not checked if ""
	}{
		{name: "concept example", in: conceptRichTextJSON},
		{name: "every mark", in: fullRichTextJSON},
		{name: "empty", in: `[]`},
		{name: "quote and backslash", in: `[{"type":"text","text":"say \"hi\" \\ bye"}]`},
		{name: "anchor only", in: linkJSON(`{"anchor":"top"}`)},
		{name: "entry and anchor", in: linkJSON(`{"entry":"ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3","anchor":"contact"}`)},
		{name: "href with fragment", in: linkJSON(`{"href":"https://example.com/about#contact","newTab":true,"rel":["nofollow"]}`)},
		{name: "path", in: linkJSON(`{"href":"/about/"}`)},

		{
			name:  "newline",
			in:    `[{"type":"text","text":"a\nb"}]`,
			paths: []string{"/0/text"},
			msg:   "contains a line break or tab; use hardBreak for line breaks",
		},
		{name: "line separator", in: `[{"type":"text","text":"a\u2028b"}]`, paths: []string{"/0/text"}, msg: "contains a line break or tab"},
		{
			name:  "NUL",
			in:    `[{"type":"text","text":"a\u0000b"}]`,
			paths: []string{"/0/text"},
			msg:   "contains control characters; normalize it first",
		},
		{
			name:  "duplicate mark",
			in:    `[{"type":"text","text":"a","marks":[{"type":"bold"},{"type":"bold"}]}]`,
			paths: []string{"/0/marks/1"},
			msg:   "duplicate bold mark",
		},
		{
			name:  "subscript and superscript",
			in:    `[{"type":"text","text":"a","marks":[{"type":"subscript"},{"type":"superscript"}]}]`,
			paths: []string{"/0/marks/1"},
			msg:   "subscript and superscript cannot be combined",
		},
		{
			name:  "superscript and subscript",
			in:    `[{"type":"text","text":"a","marks":[{"type":"superscript"},{"type":"subscript"}]}]`,
			paths: []string{"/0/marks/1"},
			msg:   "subscript and superscript cannot be combined",
		},
		{
			name:  "javascript URL",
			in:    linkJSON(`{"href":"javascript:alert(1)"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   `scheme "javascript" is not allowed`,
		},
		{
			name:  "javascript URL in mixed case",
			in:    linkJSON(`{"href":"JavaScript:alert(1)"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   `scheme "javascript" is not allowed`,
		},
		{
			name:  "protocol-relative URL",
			in:    linkJSON(`{"href":"//evil.example"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   "protocol-relative URLs are not allowed",
		},
		{
			name:  "data URL",
			in:    linkJSON(`{"href":"data:text/html,x"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   `scheme "data" is not allowed`,
		},
		{
			name:  "credentials in URL",
			in:    linkJSON(`{"href":"https://user:pass@example.com"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   "credentials in URLs are not allowed",
		},
		{
			name:  "backslash in URL",
			in:    linkJSON(`{"href":"/\\evil.example"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   "backslashes are not allowed",
		},
		{
			name:  "leading space in URL",
			in:    linkJSON(`{"href":" https://example.com"}`),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   "contains control characters or surrounding spaces; normalize it first",
		},
		{
			name:  "href and entry",
			in:    linkJSON(`{"href":"https://example.com","entry":"ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"}`),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "set at most one of href, entry and media",
		},
		{
			name:  "href, entry and media",
			in:    linkJSON(`{"href":"https://example.com","entry":"ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3","media":"med_01JB8Y6T2SB7C1D4E8F2G5H9J3"}`),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "set at most one of href, entry and media",
		},
		{
			name:  "newTab only",
			in:    linkJSON(`{"newTab":true}`),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "link needs href, entry, media or anchor",
		},
		{
			name:  "anchor and href",
			in:    linkJSON(`{"href":"https://example.com","anchor":"top"}`),
			paths: []string{"/0/marks/0/attrs/anchor"},
			msg:   "anchor cannot be combined with href; put the fragment in href",
		},
		{
			name:  "anchor and media",
			in:    linkJSON(`{"media":"med_01JB8Y6T2SB7C1D4E8F2G5H9J3","anchor":"top"}`),
			paths: []string{"/0/marks/0/attrs/anchor"},
			msg:   "anchor cannot be combined with media",
		},
		{
			name:  "entry with space",
			in:    linkJSON(`{"entry":"ent 1"}`),
			paths: []string{"/0/marks/0/attrs/entry"},
			msg:   `invalid entry ID "ent 1"`,
		},
		{
			name:  "media with slash",
			in:    linkJSON(`{"media":"med/1"}`),
			paths: []string{"/0/marks/0/attrs/media"},
			msg:   `invalid media ID "med/1"`,
		},
		{
			name:  "Japanese anchor",
			in:    linkJSON(`{"anchor":"見出し"}`),
			paths: []string{"/0/marks/0/attrs/anchor"},
			msg:   `invalid name "見出し"`,
		},
		{
			name:  "unknown rel",
			in:    linkJSON(`{"href":"https://example.com","rel":["noopener"]}`),
			paths: []string{"/0/marks/0/attrs/rel/0"},
			msg:   `unknown rel "noopener"`,
		},
		{
			name:  "duplicate rel",
			in:    linkJSON(`{"href":"https://example.com","rel":["nofollow","nofollow"]}`),
			paths: []string{"/0/marks/0/attrs/rel/1"},
			msg:   `duplicate rel "nofollow"`,
		},
		{
			name: "every link rule",
			in:   linkJSON(`{"href":"javascript:alert(1)","entry":"ent 1","media":"med/1","anchor":"a b","rel":["ugc","x","ugc"]}`),
			paths: []string{
				"/0/marks/0/attrs", "/0/marks/0/attrs/anchor", "/0/marks/0/attrs/anchor",
				"/0/marks/0/attrs/href", "/0/marks/0/attrs/entry", "/0/marks/0/attrs/media", "/0/marks/0/attrs/anchor",
				"/0/marks/0/attrs/rel/1", "/0/marks/0/attrs/rel/2",
			},
		},
		{
			name:  "variant and color",
			in:    highlightJSON(`{"variant":"marker","color":{"preset":"accent"}}`),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "variant cannot be combined with color or backgroundColor",
		},
		{
			name:  "variant with space",
			in:    highlightJSON(`{"variant":"a b"}`),
			paths: []string{"/0/marks/0/attrs/variant"},
			msg:   `invalid name "a b"`,
		},
		{
			name:  "named color",
			in:    highlightJSON(`{"color":{"value":"red"}}`),
			paths: []string{"/0/marks/0/attrs/color/value"},
			msg:   `invalid color "red"`,
		},
		{
			name:  "calc background color",
			in:    highlightJSON(`{"backgroundColor":{"value":"calc(1)"}}`),
			paths: []string{"/0/marks/0/attrs/backgroundColor/value"},
			msg:   `invalid color "calc(1)"`,
		},
		{
			name:  "color preset and value",
			in:    highlightJSON(`{"color":{"preset":"accent","value":"#fff"}}`),
			paths: []string{"/0/marks/0/attrs/color"},
			msg:   "set either preset or value, not both",
		},
		{
			name: "every highlight rule",
			in:   highlightJSON(`{"variant":"a b","color":{"preset":"x y","value":"red"},"backgroundColor":{"value":"calc(1)"}}`),
			paths: []string{
				"/0/marks/0/attrs", "/0/marks/0/attrs/variant",
				"/0/marks/0/attrs/color", "/0/marks/0/attrs/color/preset", "/0/marks/0/attrs/color/value",
				"/0/marks/0/attrs/backgroundColor/value",
			},
		},
		{
			name: "errors in document order",
			in: `[
  {"type": "text", "text": "a\tb"},
  {"type": "hardBreak"},
  {"type": "text", "text": "c", "marks": [{"type": "bold"}, {"type": "link", "attrs": {"href": "javascript:alert(1)"}}]}
]`,
			paths: []string{"/0/text", "/2/marks/1/attrs/href"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rt RichText
			if err := rt.UnmarshalJSON([]byte(tt.in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", tt.in, err)
			}
			assertRichTextValidate(t, rt, tt.paths, tt.msg)
		})
	}
}

// TestRichTextValidateGoValues checks values built in Go, some of which UnmarshalJSON rejects.
func TestRichTextValidateGoValues(t *testing.T) {
	tests := []struct {
		name  string
		in    RichText
		paths []string
		msg   string // part of the messages; not checked if ""
	}{
		{name: "nil", in: nil},
		{name: "empty marks", in: RichText{{Type: InlineText, Text: "a", Marks: []Mark{}}}},
		{name: "empty text", in: RichText{{Type: InlineText}}, paths: []string{"/0/text"}, msg: "empty text"},
		{
			name:  "NFD text",
			in:    RichText{{Type: InlineText, Text: "a"}, {Type: InlineText, Text: "e" + acute}},
			paths: []string{"/1/text"},
			msg:   "not in Unicode NFC; normalize it first",
		},
		{name: "invalid UTF-8 in text", in: RichText{{Type: InlineText, Text: "a\xffb"}}, paths: []string{"/0/text"}, msg: "invalid UTF-8"},
		{
			name:  "invalid UTF-8 in href",
			in:    markedText(Mark{Type: MarkLink, Link: &Link{Href: "https://example.com/\xff"}}),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   "invalid UTF-8",
		},
		{
			name:  "control characters before Normalize",
			in:    RichText{{Type: InlineText, Text: "a\x00b", Marks: []Mark{{Type: MarkLink, Link: &Link{Href: " https://example.com "}}}}},
			paths: []string{"/0/text", "/0/marks/0/attrs/href"},
			msg:   "normalize it first",
		},
		{
			name:  "too long text",
			in:    RichText{{Type: InlineText, Text: strings.Repeat("a", MaxTextBytes+1)}},
			paths: []string{"/0/text"},
			msg:   "too long",
		},
		{
			name:  "too long href",
			in:    markedText(Mark{Type: MarkLink, Link: &Link{Href: "/" + strings.Repeat("a", MaxTextBytes)}}),
			paths: []string{"/0/marks/0/attrs/href"},
			msg:   "too long",
		},
		{
			name:  "hardBreak with text",
			in:    RichText{{Type: InlineHardBreak, Text: "a"}},
			paths: []string{"/0/text"},
			msg:   "not allowed for hardBreak",
		},
		{
			name:  "hardBreak with marks",
			in:    RichText{{Type: InlineHardBreak, Marks: []Mark{{Type: MarkBold}}}},
			paths: []string{"/0/marks"},
			msg:   "not allowed for hardBreak",
		},
		{name: "unknown node", in: RichText{{Type: "ruby", Text: "a"}}, paths: []string{"/0/type"}, msg: `unknown inline type "ruby"`},
		{name: "link without attrs", in: markedText(Mark{Type: MarkLink}), paths: []string{"/0/marks/0/attrs"}, msg: "missing attributes"},
		{
			name:  "link with highlight attrs",
			in:    markedText(Mark{Type: MarkLink, Link: &Link{Href: "https://example.com"}, Highlight: &Highlight{Variant: "marker"}}),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "highlight attributes are not allowed for link",
		},
		{
			name:  "highlight without attrs",
			in:    markedText(Mark{Type: MarkHighlight}),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "missing attributes",
		},
		{
			name:  "highlight with link attrs",
			in:    markedText(Mark{Type: MarkHighlight, Highlight: &Highlight{Variant: "marker"}, Link: &Link{Anchor: "top"}}),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "link attributes are not allowed for highlight",
		},
		{
			name:  "bold with link attrs",
			in:    markedText(Mark{Type: MarkBold, Link: &Link{Href: "https://example.com"}}),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "not allowed for bold",
		},
		{name: "unknown mark", in: markedText(Mark{Type: "blink"}), paths: []string{"/0/marks/0/type"}, msg: `unknown mark type "blink"`},
		{
			name:  "unknown marks not checked further",
			in:    markedText(Mark{Type: "blink", Link: &Link{}}, Mark{Type: "blink"}),
			paths: []string{"/0/marks/0/type", "/0/marks/1/type"},
		},
		{
			name:  "empty highlight",
			in:    markedText(Mark{Type: MarkHighlight, Highlight: &Highlight{}}),
			paths: []string{"/0/marks/0/attrs"},
			msg:   "highlight needs variant, color or backgroundColor",
		},
		{
			name: "every mark rule",
			in: markedText(
				Mark{Type: MarkSubscript},
				Mark{Type: MarkSubscript},
				Mark{Type: MarkSuperscript, Link: &Link{Anchor: "top"}},
				Mark{Type: "blink", Link: &Link{}},
				Mark{Type: MarkLink, Highlight: &Highlight{}},
				Mark{Type: MarkLink, Link: &Link{Href: "//evil.example"}},
			),
			paths: []string{
				"/0/marks/1", "/0/marks/2", "/0/marks/2/attrs", "/0/marks/3/type",
				"/0/marks/4/attrs", "/0/marks/4/attrs", "/0/marks/5", "/0/marks/5/attrs/href",
			},
		},
		{
			name: "every node rule",
			in: RichText{
				{Type: InlineText},
				{Type: InlineHardBreak, Text: "a", Marks: []Mark{{Type: "blink"}}},
				{Type: "ruby", Marks: []Mark{{Type: "blink"}}},
				{Type: InlineText, Text: "a\x00", Marks: []Mark{{Type: "blink"}}},
			},
			paths: []string{"/0/text", "/1/text", "/1/marks", "/2/type", "/3/text", "/3/marks/0/type"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertRichTextValidate(t, tt.in, tt.paths, tt.msg)
		})
	}
}
