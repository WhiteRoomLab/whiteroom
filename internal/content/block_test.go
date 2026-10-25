package content

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// typeLast returns the JSON in with the type members of every object moved to its end, duplicates included.
func typeLast(t *testing.T, in string) string {
	t.Helper()
	opt := jsontext.AllowDuplicateNames(true)
	dec := jsontext.NewDecoder(strings.NewReader(in), opt)
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out, opt)
	must := func(err error) {
		if err != nil {
			t.Fatalf("typeLast(%.100s): %v", in, err)
		}
	}
	var value func()
	value = func() {
		kind := dec.PeekKind()
		if kind != jsontext.KindBeginObject && kind != jsontext.KindBeginArray {
			v, err := dec.ReadValue()
			must(err)
			must(enc.WriteValue(v))
			return
		}
		tok, err := dec.ReadToken()
		must(err)
		must(enc.WriteToken(tok))
		var types []jsontext.Value
		for k := dec.PeekKind(); k != jsontext.KindEndObject && k != jsontext.KindEndArray; k = dec.PeekKind() {
			if kind == jsontext.KindBeginObject {
				name, err := dec.ReadToken()
				must(err)
				if name.String() == "type" {
					v, err := dec.ReadValue()
					must(err)
					types = append(types, v.Clone())
					continue
				}
				must(enc.WriteToken(name))
			}
			value()
		}
		for _, v := range types {
			must(enc.WriteToken(jsontext.String("type")))
			must(enc.WriteValue(v))
		}
		tok, err = dec.ReadToken()
		must(err)
		must(enc.WriteToken(tok))
	}
	value()
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		t.Fatalf("typeLast(%.100s): data after the value", in)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func TestTypeLast(t *testing.T) {
	in := `[{"type": "group", "content": [{"type": "text", "text": "a", "type": "b"}], "id": "x"}, null]`
	want := `[{"content":[{"text":"a","type":"text","type":"b"}],"id":"x","type":"group"},null]`
	if got := typeLast(t, in); got != want {
		t.Errorf("typeLast(%s) = %s, want %s", in, got, want)
	}
}

// bothOrders runs read on in as written and on typeLast(t, in), each in a subtest.
func bothOrders(t *testing.T, in string, read func(t *testing.T, in string)) {
	t.Run("as written", func(t *testing.T) { read(t, in) })
	t.Run("type last", func(t *testing.T) { read(t, typeLast(t, in)) })
}

func TestBlocksUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Blocks
	}{
		{name: "empty", in: `[]`, want: nil},
		{
			name: "paragraph with every key",
			in: `[{
  "type": "paragraph",
  "id": "b_9x3v7k5m",
  "variant": "lead",
  "className": "intro",
  "anchor": "about",
  "style": {"textColor": {"preset": "primary"}, "padding": {"top": {"value": "1rem"}}},
  "attrs": {"textAlign": "center"},
  "content": [
    {"type": "text", "text": "Hello, "},
    {"type": "text", "text": "world", "marks": [{"type": "bold"}, {"type": "link", "attrs": {"href": "https://example.com"}}]}
  ]
}]`,
			want: Blocks{{
				Type:      BlockParagraph,
				ID:        "b_9x3v7k5m",
				Variant:   "lead",
				ClassName: "intro",
				Anchor:    "about",
				Style:     Style{TextColor: Color{Preset: "primary"}, Padding: Sides{Top: Length{Value: "1rem"}}},
				Attrs:     Attrs{TextAlign: "center"},
				Text: RichText{
					{Type: InlineText, Text: "Hello, "},
					{Type: InlineText, Text: "world", Marks: []Mark{{Type: MarkBold}, {Type: MarkLink, Link: &Link{Href: "https://example.com"}}}},
				},
			}},
		},
		{
			name: "table",
			in: `[{
  "type": "table",
  "id": "b_5h1j3g7d",
  "content": [
    {
      "type": "tableRow",
      "content": [
        {"type": "tableCell", "attrs": {"header": true}, "content": [{"type": "text", "text": "Address"}]},
        {"type": "tableCell", "content": [{"type": "text", "text": "1-2-3 Chiyoda, Tokyo"}]}
      ]
    }
  ]
}]`,
			want: Blocks{{
				Type: BlockTable,
				ID:   "b_5h1j3g7d",
				Children: Blocks{{
					Type: BlockTableRow,
					Children: Blocks{
						{Type: BlockTableCell, Attrs: Attrs{Header: true}, Text: RichText{{Type: InlineText, Text: "Address"}}},
						{Type: BlockTableCell, Text: RichText{{Type: InlineText, Text: "1-2-3 Chiyoda, Tokyo"}}},
					},
				}},
			}},
		},
		{
			name: "code",
			in:   `[{"type":"code","id":"b_3k7m9p2q","attrs":{"language":"go"},"content":"func main() {\n\tprintln(1)\n}"}]`,
			want: Blocks{{Type: BlockCode, ID: "b_3k7m9p2q", Attrs: Attrs{Language: "go"}, Code: "func main() {\n\tprintln(1)\n}"}},
		},
		{
			name: "nested list",
			in: `[{
  "type": "list",
  "id": "b_7w2x4y6z",
  "attrs": {"ordered": true, "start": 3},
  "content": [
    {"type": "listItem", "content": [
      {"type": "paragraph", "id": "b_1a2b3c4d", "content": [{"type": "text", "text": "One"}]},
      {"type": "list", "id": "b_5e6f7g8h", "content": [
        {"type": "listItem", "content": [{"type": "paragraph", "id": "b_9i0j1k2l", "content": [{"type": "text", "text": "One A"}]}]}
      ]}
    ]},
    {"type": "listItem", "content": [{"type": "paragraph", "id": "b_3m4n5o6p", "content": [{"type": "text", "text": "Two"}]}]}
  ]
}]`,
			want: Blocks{{
				Type:  BlockList,
				ID:    "b_7w2x4y6z",
				Attrs: Attrs{Ordered: true, Start: 3},
				Children: Blocks{
					{Type: BlockListItem, Children: Blocks{
						{Type: BlockParagraph, ID: "b_1a2b3c4d", Text: RichText{{Type: InlineText, Text: "One"}}},
						{Type: BlockList, ID: "b_5e6f7g8h", Children: Blocks{
							{Type: BlockListItem, Children: Blocks{
								{Type: BlockParagraph, ID: "b_9i0j1k2l", Text: RichText{{Type: InlineText, Text: "One A"}}},
							}},
						}},
					}},
					{Type: BlockListItem, Children: Blocks{
						{Type: BlockParagraph, ID: "b_3m4n5o6p", Text: RichText{{Type: InlineText, Text: "Two"}}},
					}},
				},
			}},
		},
		{
			name: "button and column with common keys",
			in: `[
  {"type": "buttons", "id": "b_4t6y8u0i", "content": [
    {
      "type": "button", "id": "b_2o4p6a8s", "variant": "outline", "className": "cta", "anchor": "contact",
      "style": {"backgroundColor": {"preset": "accent"}},
      "attrs": {"label": "Contact us", "link": {"anchor": "form"}}
    }
  ]},
  {"type": "columns", "id": "b_1d3f5g7h", "content": [
    {"type": "column", "id": "b_9j1k3l5z", "className": "side", "attrs": {"width": "33.33%"}, "content": [{"type": "separator", "id": "b_7x9c1v3b"}]}
  ]}
]`,
			want: Blocks{
				{Type: BlockButtons, ID: "b_4t6y8u0i", Children: Blocks{{
					Type: BlockButton, ID: "b_2o4p6a8s", Variant: "outline", ClassName: "cta", Anchor: "contact",
					Style: Style{BackgroundColor: Color{Preset: "accent"}},
					Attrs: Attrs{Label: "Contact us", Link: &Link{Anchor: "form"}},
				}}},
				{Type: BlockColumns, ID: "b_1d3f5g7h", Children: Blocks{{
					Type: BlockColumn, ID: "b_9j1k3l5z", ClassName: "side", Attrs: Attrs{Width: "33.33%"},
					Children: Blocks{{Type: BlockSeparator, ID: "b_7x9c1v3b"}},
				}}},
			},
		},
		{
			name: "blocks without content",
			in:   `[{"type":"image","id":"b_2p6t8r1z","attrs":{"media":"med_01JB8Z3K5Q","alt":"Office"}},{"type":"separator","id":"b_8s4d6f2g"},{"type":"group","id":"b_6q2w8e4r"}]`,
			want: Blocks{
				{Type: BlockImage, ID: "b_2p6t8r1z", Attrs: Attrs{Media: "med_01JB8Z3K5Q", Alt: "Office"}},
				{Type: BlockSeparator, ID: "b_8s4d6f2g"},
				{Type: BlockGroup, ID: "b_6q2w8e4r"},
			},
		},
		{name: "empty rich text", in: `[{"type":"paragraph","id":"b_9x3v7k5m","content":[]}]`, want: Blocks{{Type: BlockParagraph, ID: "b_9x3v7k5m"}}},
		{
			name: "ordered false",
			in:   `[{"type":"list","id":"b_7w2x4y6z","attrs":{"ordered":false},"content":[{"type":"listItem","content":[{"type":"paragraph","id":"b_1a2b3c4d"}]}]}]`,
			want: Blocks{{Type: BlockList, ID: "b_7w2x4y6z", Children: Blocks{{Type: BlockListItem, Children: Blocks{{Type: BlockParagraph, ID: "b_1a2b3c4d"}}}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bothOrders(t, tt.in, func(t *testing.T, in string) {
				got := Blocks{{Type: BlockSeparator}}
				if err := got.UnmarshalJSON([]byte(in)); err != nil {
					t.Fatalf("UnmarshalJSON(%s) = %v", in, err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("UnmarshalJSON(%s) = %#v, want %#v", in, got, tt.want)
				}
				// Each block on its own reads the same with Block.UnmarshalJSON.
				var elems []jsontext.Value
				if err := json.Unmarshal([]byte(in), &elems); err != nil {
					t.Fatalf("Unmarshal(%s) = %v", in, err)
				}
				for i, e := range elems {
					b := Block{Type: BlockSeparator}
					if err := b.UnmarshalJSON(e); err != nil {
						t.Fatalf("Block.UnmarshalJSON(%s) = %v", e, err)
					}
					if !reflect.DeepEqual(b, tt.want[i]) {
						t.Errorf("Block.UnmarshalJSON(%s) = %#v, want %#v", e, b, tt.want[i])
					}
				}
			})
		})
	}
}

func TestBlocksUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "null", in: `null`, path: "", msg: "expected array, got null"},
		{name: "block for blocks", in: `{"type":"separator"}`, path: "", msg: "expected array, got object"},
		{name: "null block", in: `[null]`, path: "/0", msg: "expected object, got null"},
		{name: "empty block", in: `[{}]`, path: "/0", msg: "empty object is not allowed"},
		{name: "missing type", in: `[{"id":"b_9x3v7k5m","content":[{"type":"text","text":"a"}]}]`, path: "/0", msg: `missing key "type"`},
		{name: "missing type in second block", in: `[{"type":"separator"},{"id":"b_9x3v7k5m"}]`, path: "/1", msg: `missing key "type"`},
		{name: "unknown type", in: `[{"type":"bogus","id":"b_9x3v7k5m"}]`, path: "/0/type", msg: `unknown block type "bogus"`},
		{name: "type case", in: `[{"type":"Paragraph"}]`, path: "/0/type", msg: `unknown block type "Paragraph"`},
		{name: "empty type", in: `[{"type":""}]`, path: "/0/type", msg: "empty string is not allowed"},
		{name: "number for type", in: `[{"type":1}]`, path: "/0/type", msg: "expected string, got number"},
		{name: "duplicate type", in: `[{"type":"paragraph","type":"heading"}]`, path: "/0/type", msg: "duplicate key"},
		{name: "unknown key", in: `[{"type":"paragraph","x":1}]`, path: "/0/x", msg: "unknown key"},
		{name: "key case", in: `[{"type":"paragraph","Content":[]}]`, path: "/0/Content", msg: "unknown key"},
		{name: "empty id", in: `[{"type":"paragraph","id":""}]`, path: "/0/id", msg: "empty string is not allowed"},
		{name: "unknown style key", in: `[{"type":"paragraph","style":{"shadow":{"preset":"soft"}}}]`, path: "/0/style/shadow", msg: "unknown key"},
		{
			name: "id of listItem",
			in:   `[{"type":"list","id":"b_7w2x4y6z","content":[{"type":"listItem","id":"b_9x3v7k5m","content":[{"type":"paragraph","id":"b_1a2b3c4d"}]}]}]`,
			path: "/0/content/0/id",
			msg:  "not allowed for listItem",
		},
		{
			name: "variant of tableRow",
			in:   `[{"type":"table","id":"b_5h1j3g7d","content":[{"type":"tableRow","variant":"striped","content":[{"type":"tableCell"}]}]}]`,
			path: "/0/content/0/variant",
			msg:  "not allowed for tableRow",
		},
		{
			name: "className of tableCell",
			in:   `[{"type":"table","id":"b_5h1j3g7d","content":[{"type":"tableRow","content":[{"type":"tableCell","className":"total"}]}]}]`,
			path: "/0/content/0/content/0/className",
			msg:  "not allowed for tableCell",
		},
		{
			name: "anchor of listItem",
			in:   `[{"type":"list","id":"b_7w2x4y6z","content":[{"type":"listItem","anchor":"first","content":[{"type":"paragraph","id":"b_1a2b3c4d"}]}]}]`,
			path: "/0/content/0/anchor",
			msg:  "not allowed for listItem",
		},
		{
			name: "style of tableCell",
			in:   `[{"type":"table","id":"b_5h1j3g7d","content":[{"type":"tableRow","content":[{"type":"tableCell","style":{"textColor":{"preset":"primary"}}}]}]}]`,
			path: "/0/content/0/content/0/style",
			msg:  "not allowed for tableCell",
		},
		{
			name: "style before id of listItem",
			in:   `[{"type":"list","id":"b_7w2x4y6z","content":[{"type":"listItem","style":{"textColor":{"preset":"primary"}},"id":"b_9x3v7k5m","content":[{"type":"paragraph","id":"b_1a2b3c4d"}]}]}]`,
			path: "/0/content/0/id",
			msg:  "not allowed for listItem",
		},
		{name: "attrs of separator", in: `[{"type":"separator","id":"b_9x3v7k5m","attrs":{"level":2}}]`, path: "/0/attrs", msg: "not allowed for separator"},
		{name: "level of paragraph", in: `[{"type":"paragraph","attrs":{"level":2}}]`, path: "/0/attrs/level", msg: "not allowed for paragraph"},
		{name: "unknown attribute", in: `[{"type":"paragraph","attrs":{"bogus":1}}]`, path: "/0/attrs/bogus", msg: "unknown key"},
		{name: "level 1", in: `[{"type":"heading","attrs":{"level":1}}]`, path: "/0/attrs/level", msg: "level 1 is out of range (2-6)"},
		{name: "empty attrs", in: `[{"type":"paragraph","attrs":{}}]`, path: "/0/attrs", msg: "empty object is not allowed"},
		{
			name: "content of image",
			in:   `[{"type":"image","attrs":{"media":"med_01JB8Z3K5Q"},"content":[]}]`,
			path: "/0/content",
			msg:  "not allowed for image",
		},
		{name: "content of separator", in: `[{"type":"separator","content":"x"}]`, path: "/0/content", msg: "not allowed for separator"},
		{name: "empty group", in: `[{"type":"group","content":[]}]`, path: "/0/content", msg: "empty array is not allowed"},
		{name: "empty list", in: `[{"type":"list","content":[]}]`, path: "/0/content", msg: "empty array is not allowed"},
		{name: "array for code", in: `[{"type":"code","content":[]}]`, path: "/0/content", msg: "expected string, got array"},
		{name: "empty code", in: `[{"type":"code","content":""}]`, path: "/0/content", msg: "empty string is not allowed"},
		{name: "string for rich text", in: `[{"type":"paragraph","content":"x"}]`, path: "/0/content", msg: "expected array, got string"},
		{name: "block for child blocks", in: `[{"type":"group","content":{"type":"separator"}}]`, path: "/0/content", msg: "expected array, got object"},
		{name: "null content", in: `[{"type":"group","content":null}]`, path: "/0/content", msg: "expected array, got null"},
		{name: "invalid rich text", in: `[{"type":"paragraph","content":[{"type":"text"}]}]`, path: "/0/content/0", msg: `missing key "text"`},
		{name: "null child", in: `[{"type":"group","content":[null]}]`, path: "/0/content/0", msg: "expected object, got null"},
		{
			name: "unknown type deep in content",
			in:   `[{"type":"group","content":[{"type":"group","content":[{"type":"bogus"}]}]}]`,
			path: "/0/content/0/content/0/type",
			msg:  `unknown block type "bogus"`,
		},
		{
			name: "unknown type deep in content read later",
			in:   `[{"content":[{"type":"group","content":[{"type":"bogus"}]}],"type":"group"}]`,
			path: "/0/content/0/content/0/type",
			msg:  `unknown block type "bogus"`,
		},
		{
			name: "duplicate key deep in content",
			in:   `[{"type":"group","content":[{"type":"paragraph","content":[{"type":"text","text":"a","text":"b"}]}]}]`,
			path: "/0/content/0/content/0/text",
			msg:  "duplicate key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bothOrders(t, tt.in, func(t *testing.T, in string) {
				orig := Blocks{{Type: BlockParagraph, Text: RichText{{Type: InlineText, Text: "old"}}}}
				bs := slices.Clone(orig)
				assertError(t, bs.UnmarshalJSON([]byte(in)), tt.path, tt.msg)
				if !reflect.DeepEqual(bs, orig) {
					t.Errorf("UnmarshalJSON(%s) changed the value to %#v", in, bs)
				}
			})
		})
	}
}

// nestedGroups is a block of n groups, each in the one before, with a separator in the last.
func nestedGroups(n int) string {
	return strings.Repeat(`{"type":"group","content":[`, n) + `{"type":"separator"}` + strings.Repeat(`]}`, n)
}

// separators is n separator blocks, separated by commas.
func separators(n int) string {
	return strings.TrimSuffix(strings.Repeat(`{"type":"separator"},`, n), ",")
}

// countBlocks returns the number of blocks in bs, including their descendants.
func countBlocks(bs Blocks) int {
	n := len(bs)
	for _, b := range bs {
		n += countBlocks(b.Children)
	}
	return n
}

func TestBlocksUnmarshalJSONLimits(t *testing.T) {
	const (
		tooDeep = "blocks are nested too deeply (max 32)"
		tooMany = "too many blocks (max 10000)"
	)
	group := func(n int) string { return `{"type":"group","content":[` + separators(n) + `]}` }
	tests := []struct {
		name   string
		block  bool // in is a block for Block.UnmarshalJSON, not blocks for Blocks.UnmarshalJSON
		in     string
		blocks int    // blocks read if path is ""
		path   string // path of the error
		msg    string
	}{
		{name: "deepest", in: "[" + nestedGroups(MaxDepth-1) + "]", blocks: MaxDepth},
		{name: "too deep", in: "[" + nestedGroups(MaxDepth) + "]", path: "/0" + strings.Repeat("/content/0", MaxDepth-1) + "/content", msg: tooDeep},
		{name: "most blocks", in: "[" + separators(MaxBlocks) + "]", blocks: MaxBlocks},
		{name: "too many blocks", in: "[" + separators(MaxBlocks+1) + "]", path: "/10000", msg: tooMany},
		{name: "too many blocks in a group", in: "[" + group(MaxBlocks) + "]", path: "/0/content/9999", msg: tooMany},
		{name: "block after a group of 9999", in: "[" + group(MaxBlocks-1) + `,{"type":"separator"}]`, path: "/1", msg: tooMany},
		{name: "deepest in a block", block: true, in: nestedGroups(MaxDepth - 1), blocks: MaxDepth},
		{name: "too deep in a block", block: true, in: nestedGroups(MaxDepth), path: strings.Repeat("/content/0", MaxDepth-1) + "/content", msg: tooDeep},
		{name: "most blocks in a block", block: true, in: group(MaxBlocks - 1), blocks: MaxBlocks},
		{name: "too many blocks in a block", block: true, in: group(MaxBlocks), path: "/content/9999", msg: tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bothOrders(t, tt.in, func(t *testing.T, in string) {
				var bs Blocks
				var err error
				if tt.block {
					var b Block
					err = b.UnmarshalJSON([]byte(in))
					bs = Blocks{b}
				} else {
					err = bs.UnmarshalJSON([]byte(in))
				}
				if tt.path != "" {
					assertError(t, err, tt.path, tt.msg)
					return
				}
				if err != nil {
					t.Fatalf("UnmarshalJSON(%.100s...) = %v", in, err)
				}
				if n := countBlocks(bs); n != tt.blocks {
					t.Errorf("UnmarshalJSON(%.100s...) read %d blocks, want %d", in, n, tt.blocks)
				}
			})
		})
	}
}

func TestJSONUnmarshalBlockTypes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		in   string
		path string
	}{
		{name: "Blocks", v: new(Blocks), in: `[{"type":"separator","x":1}]`, path: "/0/x"},
		{name: "Block", v: new(Block), in: `{"type":"paragraph","Text":[]}`, path: "/Text"},
		{name: "Attrs in Block", v: new(Block), in: `{"type":"heading","attrs":{"Level":2}}`, path: "/attrs/Level"},
		{name: "content read later", v: new(Blocks), in: `[{"content":[{"type":"separator","x":1}],"type":"group"}]`, path: "/0/content/0/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, json.Unmarshal([]byte(tt.in), tt.v), tt.path, "unknown key")
		})
	}
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBlockRoundTrip(t *testing.T) {
	for _, name := range []string{"block-example.json", "wordpress-paragraph.json"} {
		t.Run(name, func(t *testing.T) {
			want := readTestdata(t, name)
			bothOrders(t, want, func(t *testing.T, in string) { assertRoundTrip[Block](t, in, want) })
		})
	}
}

func TestBlocksRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "about page body", in: readTestdata(t, "about-page-body.json")},
		{name: "every block", in: readTestdata(t, "every-block.json")},
		{name: "empty", in: `[]`},
		{
			name: "reversed key order",
			in: `[
  {
    "content": [
      {"text": "Hello, ", "type": "text"},
      {"marks": [{"type": "bold"}, {"attrs": {"newTab": true, "href": "https://example.com"}, "type": "link"}], "text": "world", "type": "text"}
    ],
    "attrs": {"textAlign": "center"},
    "style": {"padding": {"bottom": {"value": "2rem"}, "top": {"value": "1rem"}}, "textColor": {"preset": "primary"}},
    "anchor": "about",
    "className": "intro",
    "variant": "lead",
    "id": "b_9x3v7k5m",
    "type": "paragraph"
  },
  {
    "attrs": {
      "align": "wide", "aspectRatio": "16/9", "width": "300px", "size": "large",
      "link": {"newTab": true, "href": "https://example.com/office"},
      "caption": [{"text": "Head office", "type": "text"}],
      "alt": "Our office building", "media": "med_01JB8Z3K5Q"
    },
    "id": "b_2p6t8r1z",
    "type": "image"
  },
  {
    "content": [
      {"content": [
        {"content": [{"text": "Address", "type": "text"}], "attrs": {"textAlign": "left", "rowspan": 2, "colspan": 3, "header": true}, "type": "tableCell"}
      ], "attrs": {"section": "head"}, "type": "tableRow"}
    ],
    "attrs": {"caption": [{"text": "Company profile", "type": "text"}]},
    "id": "b_5h1j3g7d",
    "type": "table"
  },
  {
    "content": [
      {"content": [{"id": "b_7x9c1v3b", "type": "separator"}], "attrs": {"verticalAlign": "stretch", "width": "33.33%"}, "id": "b_9j1k3l5z", "type": "column"}
    ],
    "attrs": {"stackOnMobile": true, "verticalAlign": "top"},
    "id": "b_1d3f5g7h",
    "type": "columns"
  },
  {
    "content": [{"content": "x := 1", "attrs": {"language": "go"}, "id": "b_3k7m9p2q", "type": "code"}],
    "attrs": {
      "align": "full", "label": "Notice", "tagName": "section",
      "layout": {"nowrap": true, "verticalAlign": "center", "justify": "spaceBetween", "type": "row"}
    },
    "id": "b_6q2w8e4r",
    "type": "group"
  }
]`,
			want: `[
  {
    "type": "paragraph",
    "id": "b_9x3v7k5m",
    "variant": "lead",
    "className": "intro",
    "anchor": "about",
    "style": {"textColor": {"preset": "primary"}, "padding": {"top": {"value": "1rem"}, "bottom": {"value": "2rem"}}},
    "attrs": {"textAlign": "center"},
    "content": [
      {"type": "text", "text": "Hello, "},
      {"type": "text", "text": "world", "marks": [{"type": "bold"}, {"type": "link", "attrs": {"href": "https://example.com", "newTab": true}}]}
    ]
  },
  {
    "type": "image",
    "id": "b_2p6t8r1z",
    "attrs": {
      "media": "med_01JB8Z3K5Q", "alt": "Our office building",
      "caption": [{"type": "text", "text": "Head office"}],
      "link": {"href": "https://example.com/office", "newTab": true},
      "size": "large", "width": "300px", "aspectRatio": "16/9", "align": "wide"
    }
  },
  {
    "type": "table",
    "id": "b_5h1j3g7d",
    "attrs": {"caption": [{"type": "text", "text": "Company profile"}]},
    "content": [
      {"type": "tableRow", "attrs": {"section": "head"}, "content": [
        {"type": "tableCell", "attrs": {"header": true, "colspan": 3, "rowspan": 2, "textAlign": "left"}, "content": [{"type": "text", "text": "Address"}]}
      ]}
    ]
  },
  {
    "type": "columns",
    "id": "b_1d3f5g7h",
    "attrs": {"verticalAlign": "top", "stackOnMobile": true},
    "content": [
      {"type": "column", "id": "b_9j1k3l5z", "attrs": {"width": "33.33%", "verticalAlign": "stretch"}, "content": [{"type": "separator", "id": "b_7x9c1v3b"}]}
    ]
  },
  {
    "type": "group",
    "id": "b_6q2w8e4r",
    "attrs": {
      "layout": {"type": "row", "justify": "spaceBetween", "verticalAlign": "center", "nowrap": true},
      "tagName": "section", "label": "Notice", "align": "full"
    },
    "content": [{"type": "code", "id": "b_3k7m9p2q", "attrs": {"language": "go"}, "content": "x := 1"}]
  }
]`,
		},
		{
			name: "ordered false",
			in:   `[{"type":"list","id":"b_7w2x4y6z","attrs":{"ordered":false},"content":[{"type":"listItem","content":[{"type":"paragraph","id":"b_1a2b3c4d"}]}]}]`,
			want: `[{"type":"list","id":"b_7w2x4y6z","content":[{"type":"listItem","content":[{"type":"paragraph","id":"b_1a2b3c4d"}]}]}]`,
		},
		{
			name: "decorative false",
			in:   `[{"type":"image","id":"b_2p6t8r1z","attrs":{"media":"med_01JB8Z3K5Q","alt":"Office","decorative":false}}]`,
			want: `[{"type":"image","id":"b_2p6t8r1z","attrs":{"media":"med_01JB8Z3K5Q","alt":"Office"}}]`,
		},
		{
			name: "header false",
			in:   `[{"type":"table","id":"b_5h1j3g7d","content":[{"type":"tableRow","content":[{"type":"tableCell","attrs":{"header":false,"colspan":2}}]}]}]`,
			want: `[{"type":"table","id":"b_5h1j3g7d","content":[{"type":"tableRow","content":[{"type":"tableCell","attrs":{"colspan":2}}]}]}]`,
		},
		{
			name: "nowrap false",
			in:   `[{"type":"group","id":"b_6q2w8e4r","attrs":{"layout":{"type":"row","nowrap":false}}}]`,
			want: `[{"type":"group","id":"b_6q2w8e4r","attrs":{"layout":{"type":"row"}}}]`,
		},
		{
			name: "empty content of paragraph",
			in:   `[{"type":"paragraph","id":"b_9x3v7k5m","content":[]}]`,
			want: `[{"type":"paragraph","id":"b_9x3v7k5m"}]`,
		},
		{
			name: "empty caption",
			in:   `[{"type":"image","id":"b_2p6t8r1z","attrs":{"media":"med_01JB8Z3K5Q","alt":"Office","caption":[]}}]`,
			want: `[{"type":"image","id":"b_2p6t8r1z","attrs":{"media":"med_01JB8Z3K5Q","alt":"Office"}}]`,
		},
		{
			name: "only an empty caption",
			in:   `[{"type":"table","id":"b_5h1j3g7d","attrs":{"caption":[]},"content":[{"type":"tableRow","content":[{"type":"tableCell"}]}]}]`,
			want: `[{"type":"table","id":"b_5h1j3g7d","content":[{"type":"tableRow","content":[{"type":"tableCell"}]}]}]`,
		},
		{
			name: "stackOnMobile false",
			in:   `[{"type":"columns","id":"b_1d3f5g7h","attrs":{"stackOnMobile":false},"content":[{"type":"column","id":"b_9j1k3l5z"}]}]`,
		},
		{
			name: "stackOnMobile true",
			in:   `[{"type":"columns","id":"b_1d3f5g7h","attrs":{"stackOnMobile":true},"content":[{"type":"column","id":"b_9j1k3l5z"}]}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			bothOrders(t, tt.in, func(t *testing.T, in string) { assertRoundTrip[Blocks](t, in, want) })
		})
	}
}

func TestJSONMarshalBlockTypes(t *testing.T) {
	text := RichText{{Type: InlineText, Text: "a"}}
	children := Blocks{{Type: BlockSeparator, ID: "b_8s4d6f2g"}}
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "nil blocks", in: Blocks(nil), want: `[]`},
		{name: "empty blocks", in: Blocks{}, want: `[]`},
		{
			name: "blocks",
			in:   Blocks{{Type: BlockGroup, ID: "b_6q2w8e4r", Children: children}, {Type: BlockSeparator, ID: "b_7x9c1v3b"}},
			want: `[{"type":"group","id":"b_6q2w8e4r","content":[{"type":"separator","id":"b_8s4d6f2g"}]},{"type":"separator","id":"b_7x9c1v3b"}]`,
		},
		{
			name: "children of paragraph",
			in:   Block{Type: BlockParagraph, ID: "b_9x3v7k5m", Children: children},
			want: `{"type":"paragraph","id":"b_9x3v7k5m"}`,
		},
		{
			name: "text of image",
			in:   Block{Type: BlockImage, ID: "b_2p6t8r1z", Attrs: Attrs{Media: "med_01JB8Z3K5Q", Alt: "Office"}, Text: text},
			want: `{"type":"image","id":"b_2p6t8r1z","attrs":{"media":"med_01JB8Z3K5Q","alt":"Office"}}`,
		},
		{
			name: "children and code of tableCell",
			in:   Block{Type: BlockTableCell, Text: text, Children: children, Code: "x"},
			want: `{"type":"tableCell","content":[{"type":"text","text":"a"}]}`,
		},
		{
			name: "text and code of group",
			in:   Block{Type: BlockGroup, ID: "b_6q2w8e4r", Text: text, Children: children, Code: "x"},
			want: `{"type":"group","id":"b_6q2w8e4r","content":[{"type":"separator","id":"b_8s4d6f2g"}]}`,
		},
		{
			name: "text and children of code",
			in:   Block{Type: BlockCode, ID: "b_3k7m9p2q", Text: text, Children: children, Code: "x"},
			want: `{"type":"code","id":"b_3k7m9p2q","content":"x"}`,
		},
		{
			name: "unknown type",
			in:   Block{Type: "bogus", ID: "b_9x3v7k5m", Text: text, Children: children, Code: "x"},
			want: `{"type":"bogus","id":"b_9x3v7k5m"}`,
		},
		{name: "empty rich text", in: Block{Type: BlockParagraph, ID: "b_9x3v7k5m", Text: RichText{}}, want: `{"type":"paragraph","id":"b_9x3v7k5m"}`},
		{name: "empty children", in: Block{Type: BlockGroup, ID: "b_6q2w8e4r", Children: Blocks{}}, want: `{"type":"group","id":"b_6q2w8e4r"}`},
		{
			name: "empty code",
			in:   Block{Type: BlockCode, ID: "b_3k7m9p2q", Attrs: Attrs{Language: "go"}},
			want: `{"type":"code","id":"b_3k7m9p2q","attrs":{"language":"go"}}`,
		},
		{
			name: "only an empty caption",
			in:   Block{Type: BlockImage, ID: "b_2p6t8r1z", Attrs: Attrs{Caption: RichText{}}},
			want: `{"type":"image","id":"b_2p6t8r1z"}`,
		},
		{
			name: "stackOnMobile false",
			in:   Block{Type: BlockColumns, ID: "b_1d3f5g7h", Attrs: Attrs{StackOnMobile: new(false)}},
			want: `{"type":"columns","id":"b_1d3f5g7h","attrs":{"stackOnMobile":false}}`,
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

func TestEveryBlockCoverage(t *testing.T) {
	var bs Blocks
	if err := bs.UnmarshalJSON([]byte(readTestdata(t, "every-block.json"))); err != nil {
		t.Fatalf("UnmarshalJSON(every-block.json) = %v", err)
	}
	attrs := make(map[BlockType]map[string]bool)
	ids := make(map[string]bool)
	var walk func(bs Blocks)
	walk = func(bs Blocks) {
		for _, b := range bs {
			if attrs[b.Type] == nil {
				attrs[b.Type] = make(map[string]bool)
			}
			for _, k := range b.Attrs.keys() {
				attrs[b.Type][k] = true
			}
			if !blockSpecs[b.Type].part {
				if err := checkBlockID(b.ID); err != nil {
					t.Error(err)
				} else if ids[b.ID] {
					t.Errorf("duplicate block ID %q", b.ID)
				}
				ids[b.ID] = true
			}
			walk(b.Children)
		}
	}
	walk(bs)
	for typ, spec := range blockSpecs {
		seen, ok := attrs[typ]
		if !ok {
			t.Errorf("no %s block", typ)
			continue
		}
		if got, want := slices.Sorted(maps.Keys(seen)), slices.Sorted(slices.Values(spec.attrs)); !slices.Equal(got, want) {
			t.Errorf("attributes of the %s blocks = %q, want %q", typ, got, want)
		}
	}
}

func TestBlocksNormalize(t *testing.T) {
	const media = "med_01JB8Z3K5Q"
	// rt is a rich text of a text s, or nil if s is "", as Normalize leaves a rich text without texts.
	rt := func(s string) RichText {
		if s == "" {
			return nil
		}
		return RichText{{Type: InlineText, Text: s}}
	}
	// texts is blocks with s in each text that Normalize puts in NFC, some of them in child blocks.
	texts := func(s string) Blocks {
		return Blocks{
			{Type: BlockHeading, ID: "b_0heading", Attrs: Attrs{Level: 2}, Text: rt(s)},
			{Type: BlockQuote, ID: "b_000quote", Attrs: Attrs{Citation: rt(s)}, Children: Blocks{{Type: BlockParagraph, ID: "b_0paragr1", Text: rt(s)}}},
			{Type: BlockImage, ID: "b_000image", Attrs: Attrs{Media: media, Alt: s, Caption: rt(s)}},
			{
				Type: BlockTable, ID: "b_000table", Attrs: Attrs{Caption: rt(s)},
				Children: Blocks{{Type: BlockTableRow, Children: Blocks{{Type: BlockTableCell, Text: rt(s)}}}},
			},
			{Type: BlockButtons, ID: "b_0buttons", Children: Blocks{{Type: BlockButton, ID: "b_0button1", Attrs: Attrs{Label: s}}}},
			{Type: BlockLegacy, ID: "b_00legacy", Attrs: Attrs{HTML: "[gallery]", Reason: s}},
			{Type: BlockCode, ID: "b_0000code", Code: s},
		}
	}
	// lines is blocks with s as an alt, a label and a reason, which are lines of text.
	lines := func(s string) Blocks {
		return Blocks{
			{Type: BlockImage, ID: "b_000image", Attrs: Attrs{Media: media, Alt: s}},
			{Type: BlockButtons, ID: "b_0buttons", Children: Blocks{{Type: BlockButton, ID: "b_0button1", Attrs: Attrs{Label: s}}}},
			{Type: BlockLegacy, ID: "b_00legacy", Attrs: Attrs{HTML: "[gallery]", Reason: s}},
		}
	}
	// html is an html block and a legacy block with s as their HTML.
	html := func(s string) Blocks {
		return Blocks{
			{Type: BlockHTML, ID: "b_0000html", Attrs: Attrs{HTML: s}},
			{Type: BlockLegacy, ID: "b_00legacy", Attrs: Attrs{HTML: s, Reason: "shortcode gallery"}},
		}
	}
	// urls is an embed with the URL embed, and an image and a button with links to image and button.
	urls := func(embed, image, button string) Blocks {
		return Blocks{
			{Type: BlockEmbed, ID: "b_000embed", Attrs: Attrs{Provider: "youtube", URL: embed}},
			{Type: BlockImage, ID: "b_000image", Attrs: Attrs{Media: media, Alt: "Office", Link: &Link{Href: image, NewTab: true}}},
			{Type: BlockButtons, ID: "b_0buttons", Children: Blocks{{Type: BlockButton, ID: "b_0button1", Attrs: Attrs{Label: "Go", Link: &Link{Href: button}}}}},
		}
	}
	// unused is blocks with the text s, the HTML and code ml and the URL u in fields that their types do not use.
	unused := func(s, ml, u string) Blocks {
		return Blocks{
			{
				Type: BlockParagraph, ID: "b_0paragr1",
				Attrs:    Attrs{URL: u, Alt: s, Caption: rt(s), Link: &Link{Href: u}, HTML: ml},
				Children: Blocks{{Type: BlockParagraph, ID: "b_0paragr2", Text: rt(s)}},
				Code:     ml,
			},
			{Type: "bogus", Text: rt(s), Code: ml},
		}
	}
	// names is an image whose names and style have spaces and control characters, which Normalize leaves as they are.
	names := func() Blocks {
		return Blocks{{
			Type: BlockImage, ID: "b_000image ", Variant: " lead", ClassName: "a\x00", Anchor: " x",
			Style: Style{TextColor: Color{Value: " #fff"}},
			Attrs: Attrs{Media: " " + media, Alt: "Office", Size: "large\t", Link: &Link{Entry: " ent_01JB8Y6T2R", Anchor: "top\n"}},
		}}
	}
	// columns is a columns block with stackOnMobile true, the same as omitting it.
	columns := func() Blocks {
		return Blocks{{Type: BlockColumns, ID: "b_0columns", Attrs: Attrs{StackOnMobile: new(true)}, Children: Blocks{{Type: BlockColumn, ID: "b_0column1"}}}}
	}
	every := readTestdata(t, "every-block.json")
	tests := []struct {
		name  string
		in    Blocks
		want  Blocks
		paths []string // problems that Validate finds after Normalize
		msg   string   // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "nil", in: nil, want: nil},
		{name: "NFD", in: texts("e" + acute), want: texts(eAcute)},
		{name: "NUL", in: texts("a\x00b"), want: texts("ab")},
		{name: "invalid UTF-8", in: texts("a\xffb"), want: texts("a" + string(rune(0xFFFD)) + "b")},
		{
			name:  "texts left empty",
			in:    texts("\x00\x7f"),
			want:  texts(""),
			paths: []string{"/0/content", "/4/content/0/attrs/label"},
			msg:   "/0/content: empty heading\n/4/content/0/attrs/label: missing label",
		},
		{
			name:  "line breaks in lines of text",
			in:    lines("a\r\nb\x00"),
			want:  lines("a\r\nb"),
			paths: []string{"/0/attrs/alt", "/1/content/0/attrs/label", "/2/attrs/reason"},
			msg:   "/0/attrs/alt: contains a line break or tab",
		},
		{
			name: "line breaks in code",
			in:   Blocks{{Type: BlockCode, ID: "b_0000code", Code: "a\r\nb\rc\n\td"}},
			want: Blocks{{Type: BlockCode, ID: "b_0000code", Code: "a\nb\nc\n\td"}},
		},
		{
			name: "HTML",
			in:   html(`<img src="/cafe` + acute + `.jpg">` + "\r\n\t<p>a\x00b</p>\r"),
			want: html(`<img src="/cafe` + acute + `.jpg">` + "\n\t<p>ab</p>\n"),
		},
		{
			name: "URLs",
			in:   urls(" https://youtu.be/dQw4w9WgXcQ\n", "\thttps://exa\x00mple.com/office ", " /contact"),
			want: urls("https://youtu.be/dQw4w9WgXcQ", "https://example.com/office", "/contact"),
		},
		{
			name:  "URLs left empty",
			in:    urls(" \n", "\x00", "\t"),
			want:  urls("", "", ""),
			paths: []string{"/0/attrs/url", "/1/attrs/link", "/2/content/0/attrs/link"},
			msg:   "/0/attrs/url: missing url",
		},
		{
			name:  "fields that the type does not use",
			in:    unused("e"+acute, "a\r\n", " /a"),
			want:  unused(eAcute, "a\n", "/a"),
			paths: []string{"/0/attrs/url", "/0/attrs/alt", "/0/attrs/caption", "/0/attrs/link", "/0/attrs/html", "/0/content", "/0/content", "/1/type"},
		},
		{
			name: "names and style",
			in:   names(),
			want: names(),
			paths: []string{
				"/0/id", "/0/variant", "/0/className", "/0/anchor", "/0/style/textColor/value",
				"/0/attrs/media", "/0/attrs/link/entry", "/0/attrs/link/anchor", "/0/attrs/size",
			},
			msg: `/0/anchor: invalid name " x"`,
		},
		{name: "stackOnMobile true", in: columns(), want: columns()},
		{name: "every block", in: mustBlocks(t, every), want: mustBlocks(t, every)},
	}
	// AllowHTML and Import accept the html and legacy blocks and the images without alt.
	opts := ValidateOptions{AllowHTML: true, Import: true}
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
			assertBlocksValidate(t, got, opts, tt.paths, tt.msg)
			if err := got.Validate(opts); err != nil && strings.Contains(err.Error(), "normalize it first") {
				t.Errorf("Validate() = %v, want no problem that Normalize fixes", err)
			}
		})
	}
}
