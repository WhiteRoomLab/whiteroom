package content

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// mustBlocks reads in with Blocks.UnmarshalJSON.
func mustBlocks(t *testing.T, in string) Blocks {
	t.Helper()
	var bs Blocks
	if err := bs.UnmarshalJSON([]byte(in)); err != nil {
		t.Fatalf("UnmarshalJSON(%s) = %v", in, err)
	}
	return bs
}

// assertBlocksValidate checks the paths of the problems that Validate(opts) finds in bs and, if msg is not "", their messages.
// It also checks that bs.check(c, "/body") reports the same paths under "/body".
func assertBlocksValidate(t *testing.T, bs Blocks, opts ValidateOptions, paths []string, msg string) {
	t.Helper()
	err := bs.Validate(opts)
	if paths == nil && err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if got := errorPaths(t, err); !reflect.DeepEqual(got, paths) {
		t.Errorf("Validate() paths = %q, want %q", got, paths)
	}
	if msg != "" {
		assertErrorMsg(t, err, msg)
	}
	c := checker{doc: &docState{opts: opts}}
	bs.check(&c, "/body")
	var want []string
	for _, p := range paths {
		want = append(want, "/body"+p)
	}
	if got := errorPaths(t, c.result()); !reflect.DeepEqual(got, want) {
		t.Errorf(`check(c, "/body") paths = %q, want %q`, got, want)
	}
}

// testID returns a block ID that differs for each i.
func testID(i int) string {
	return fmt.Sprintf("b_%08d", i)
}

// separatorBlocks returns n separators with the IDs testID(from), testID(from+1) and so on.
func separatorBlocks(from, n int) Blocks {
	bs := make(Blocks, n)
	for i := range bs {
		bs[i] = Block{Type: BlockSeparator, ID: testID(from + i)}
	}
	return bs
}

func TestValidateTestdata(t *testing.T) {
	tests := []struct {
		file  string
		block bool // the file has a block, which is checked as Blocks{b}
		opts  ValidateOptions
	}{
		{file: "about-page-body.json"},
		{file: "block-example.json", block: true},
		{file: "wordpress-paragraph.json", block: true},
		{file: "every-block.json", opts: ValidateOptions{AllowHTML: true, Import: true}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			in := readTestdata(t, tt.file)
			if !tt.block {
				assertBlocksValidate(t, mustBlocks(t, in), tt.opts, nil, "")
				return
			}
			var b Block
			if err := b.UnmarshalJSON([]byte(in)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) = %v", in, err)
			}
			assertBlocksValidate(t, Blocks{b}, tt.opts, nil, "")
		})
	}
}

// listItemJSON is a list with a listItem whose content is blocks.
func listItemJSON(blocks string) string {
	return `[{"type":"list","id":"b_00list01","content":[{"type":"listItem","content":[` + blocks + `]}]}]`
}

// columnJSON is a columns block with a column whose content is blocks.
func columnJSON(blocks string) string {
	return `[{"type":"columns","id":"b_0columns","content":[{"type":"column","id":"b_0column1","content":[` + blocks + `]}]}]`
}

// tableJSON is a table with a row of each section ("" for none), each row with a cell.
func tableJSON(sections ...string) string {
	rows := make([]string, len(sections))
	for i, s := range sections {
		attrs := ""
		if s != "" {
			attrs = `"attrs":{"section":"` + s + `"},`
		}
		rows[i] = `{"type":"tableRow",` + attrs + `"content":[{"type":"tableCell"}]}`
	}
	return `[{"type":"table","id":"b_000table","content":[` + strings.Join(rows, ",") + `]}]`
}

func TestBlocksValidate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "empty", in: `[]`},
		{
			name: "group and column without content",
			in:   `[{"type":"group","id":"b_00group1"},{"type":"columns","id":"b_0columns","content":[{"type":"column","id":"b_0column1"}]}]`,
		},
		{
			name: "listItem with a paragraph and a list",
			in: listItemJSON(`{"type":"paragraph","id":"b_0paragr1"},` +
				`{"type":"list","id":"b_00list02","content":[{"type":"listItem","content":[{"type":"paragraph","id":"b_0paragr2"}]}]}`),
		},
		{
			name: "columns after columns and in a group",
			in: `[
  {"type":"columns","id":"b_0columns","content":[{"type":"column","id":"b_0column1"}]},
  {"type":"group","id":"b_00group1","content":[{"type":"columns","id":"b_columns2","content":[{"type":"column","id":"b_0column2"}]}]}
]`,
		},
		{name: "table rows in order", in: tableJSON("head", "head", "", "body", "foot", "foot")},
		{
			name: "anchors named as IDs",
			in:   `[{"type":"paragraph","id":"b_9x3v7k5m","anchor":"b_4n8c1w2e"},{"type":"paragraph","id":"b_4n8c1w2e","anchor":"b_9x3v7k5m"}]`,
		},
		{name: "code", in: `[{"type":"code","id":"b_0000code","content":"if a {\n\tb()\n}\n"}]`},

		{
			name:  "listItem at the top level",
			in:    `[{"type":"listItem","content":[{"type":"paragraph","id":"b_0paragr1"}]}]`,
			paths: []string{"/0"},
			msg:   "/0: listItem must be inside list",
		},
		{
			name:  "tableRow in a group",
			in:    `[{"type":"group","id":"b_00group1","content":[{"type":"tableRow","content":[{"type":"tableCell"}]}]}]`,
			paths: []string{"/0/content/0"},
			msg:   "/0/content/0: tableRow must be inside table",
		},
		{
			name:  "tableCell in a quote",
			in:    `[{"type":"quote","id":"b_000quote","content":[{"type":"tableCell"}]}]`,
			paths: []string{"/0/content/0"},
			msg:   "/0/content/0: tableCell must be inside tableRow",
		},
		{
			name:  "button at the top level",
			in:    `[{"type":"button","id":"b_0button1","attrs":{"label":"Go"}}]`,
			paths: []string{"/0"},
			msg:   "/0: button must be inside buttons",
		},
		{
			name:  "column at the top level",
			in:    `[{"type":"column","id":"b_0column1"}]`,
			paths: []string{"/0"},
			msg:   "/0: column must be inside columns",
		},
		{
			name:  "column in a column",
			in:    columnJSON(`{"type":"column","id":"b_0column2"}`),
			paths: []string{"/0/content/0/content/0"},
			msg:   "/0/content/0/content/0: column must be inside columns",
		},
		{
			name:  "paragraph in a list",
			in:    `[{"type":"list","id":"b_00list01","content":[{"type":"paragraph","id":"b_0paragr1"}]}]`,
			paths: []string{"/0/content/0"},
			msg:   "/0/content/0: list can only contain listItem",
		},
		{
			name:  "tableCell in a table",
			in:    `[{"type":"table","id":"b_000table","content":[{"type":"tableCell"}]}]`,
			paths: []string{"/0/content/0"},
			msg:   "/0/content/0: table can only contain tableRow",
		},
		{
			name:  "paragraph in a tableRow",
			in:    `[{"type":"table","id":"b_000table","content":[{"type":"tableRow","content":[{"type":"paragraph","id":"b_0paragr1"}]}]}]`,
			paths: []string{"/0/content/0/content/0"},
			msg:   "/0/content/0/content/0: tableRow can only contain tableCell",
		},
		{
			name:  "paragraph in buttons",
			in:    `[{"type":"buttons","id":"b_0buttons","content":[{"type":"paragraph","id":"b_0paragr1"}]}]`,
			paths: []string{"/0/content/0"},
			msg:   "/0/content/0: buttons can only contain button",
		},
		{
			name:  "group in columns",
			in:    `[{"type":"columns","id":"b_0columns","content":[{"type":"group","id":"b_00group1"}]}]`,
			paths: []string{"/0/content/0"},
			msg:   "/0/content/0: columns can only contain column",
		},
		{
			name:  "misplaced blocks checked",
			in:    `[{"type":"listItem","content":[{"type":"paragraph"}]},{"type":"button","attrs":{"label":"Go"}}]`,
			paths: []string{"/0", "/0/content/0/id", "/1", "/1/id"},
			msg:   "/0/content/0/id: missing block ID",
		},

		{
			name:  "listItem starting with a list",
			in:    listItemJSON(`{"type":"list","id":"b_00list02","content":[{"type":"listItem","content":[{"type":"paragraph","id":"b_0paragr1"}]}]}`),
			paths: []string{"/0/content/0/content/0"},
			msg:   "/0/content/0/content/0: listItem must start with a paragraph",
		},
		{
			name:  "two paragraphs in a listItem",
			in:    listItemJSON(`{"type":"paragraph","id":"b_0paragr1"},{"type":"paragraph","id":"b_0paragr2"}`),
			paths: []string{"/0/content/0/content/1"},
			msg:   "/0/content/0/content/1: listItem can only contain a paragraph and a list",
		},
		{
			name: "third block in a listItem",
			in: listItemJSON(`{"type":"paragraph","id":"b_0paragr1"},` +
				`{"type":"list","id":"b_00list02","content":[{"type":"listItem","content":[{"type":"paragraph","id":"b_0paragr2"}]}]},` +
				`{"type":"paragraph","id":"b_0paragr3"}`),
			paths: []string{"/0/content/0/content/2"},
			msg:   "/0/content/0/content/2: listItem can only contain a paragraph and a list",
		},
		{
			name:  "listItem in a listItem",
			in:    listItemJSON(`{"type":"listItem","content":[{"type":"paragraph","id":"b_0paragr1"}]}`),
			paths: []string{"/0/content/0/content/0"},
			msg:   "/0/content/0/content/0: listItem must start with a paragraph",
		},
		{
			name:  "tableCell in a listItem",
			in:    listItemJSON(`{"type":"paragraph","id":"b_0paragr1"},{"type":"tableCell"}`),
			paths: []string{"/0/content/0/content/1"},
			msg:   "/0/content/0/content/1: listItem can only contain a paragraph and a list",
		},

		{
			name:  "columns in a column",
			in:    columnJSON(`{"type":"columns","id":"b_columns2","content":[{"type":"column","id":"b_0column2"}]}`),
			paths: []string{"/0/content/0/content/0"},
			msg:   "/0/content/0/content/0: columns cannot be nested inside columns",
		},
		{
			name: "columns in a group in a column",
			in: columnJSON(`{"type":"group","id":"b_00group1","content":[` +
				`{"type":"columns","id":"b_columns2","content":[{"type":"column","id":"b_0column2"}]}]}`),
			paths: []string{"/0/content/0/content/0/content/0"},
			msg:   "/0/content/0/content/0/content/0: columns cannot be nested inside columns",
		},
		{
			name:  "columns in columns",
			in:    `[{"type":"columns","id":"b_0columns","content":[{"type":"columns","id":"b_columns2","content":[{"type":"column","id":"b_0column2"}]}]}]`,
			paths: []string{"/0/content/0", "/0/content/0"},
			msg:   "/0/content/0: columns can only contain column\n/0/content/0: columns cannot be nested inside columns",
		},

		{
			name:  "list without listItem",
			in:    `[{"type":"list","id":"b_00list01"}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: list needs at least one listItem",
		},
		{
			name:  "listItem without content",
			in:    `[{"type":"list","id":"b_00list01","content":[{"type":"listItem"}]}]`,
			paths: []string{"/0/content/0/content"},
			msg:   "/0/content/0/content: listItem needs at least one paragraph",
		},
		{
			name:  "quote without content",
			in:    `[{"type":"quote","id":"b_000quote"}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: quote needs at least one block",
		},
		{
			name:  "table without rows",
			in:    `[{"type":"table","id":"b_000table"}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: table needs at least one tableRow",
		},
		{
			name:  "tableRow without cells",
			in:    `[{"type":"table","id":"b_000table","content":[{"type":"tableRow"}]}]`,
			paths: []string{"/0/content/0/content"},
			msg:   "/0/content/0/content: tableRow needs at least one tableCell",
		},
		{
			name:  "buttons without button",
			in:    `[{"type":"buttons","id":"b_0buttons"}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: buttons needs at least one button",
		},
		{
			name:  "columns without column",
			in:    `[{"type":"columns","id":"b_0columns"}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: columns needs at least one column",
		},

		{
			name:  "empty heading",
			in:    `[{"type":"heading","id":"b_0heading","attrs":{"level":2}}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: empty heading",
		},
		{
			name:  "empty content of a heading",
			in:    `[{"type":"heading","id":"b_0heading","attrs":{"level":2},"content":[]}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: empty heading",
		},
		{
			name:  "tab in a heading",
			in:    `[{"type":"heading","id":"b_0heading","attrs":{"level":2},"content":[{"type":"text","text":"a\tb"}]}]`,
			paths: []string{"/0/content/0/text"},
			msg:   "/0/content/0/text: contains a line break or tab",
		},
		{
			name: "javascript URL in a tableCell",
			in: `[{"type":"table","id":"b_000table","content":[{"type":"tableRow","content":[{"type":"tableCell","content":[` +
				`{"type":"text","text":"a","marks":[{"type":"link","attrs":{"href":"javascript:alert(1)"}}]}]}]}]}]`,
			paths: []string{"/0/content/0/content/0/content/0/marks/0/attrs/href"},
			msg:   `scheme "javascript" is not allowed`,
		},
		{
			name:  "CR in code",
			in:    `[{"type":"code","id":"b_0000code","content":"a\r\nb"}]`,
			paths: []string{"/0/content"},
			msg:   "/0/content: contains control characters; normalize it first",
		},

		{name: "missing ID", in: `[{"type":"paragraph"}]`, paths: []string{"/0/id"}, msg: "/0/id: missing block ID"},
		{
			name: "missing IDs of button and column",
			in: `[{"type":"buttons","id":"b_0buttons","content":[{"type":"button","attrs":{"label":"Go"}}]},` +
				`{"type":"columns","id":"b_0columns","content":[{"type":"column"}]}]`,
			paths: []string{"/0/content/0/id", "/1/content/0/id"},
			msg:   "/1/content/0/id: missing block ID",
		},
		{
			name:  "uppercase ID",
			in:    `[{"type":"paragraph","id":"b_9X3V7K5M"}]`,
			paths: []string{"/0/id"},
			msg:   `/0/id: invalid block ID "b_9X3V7K5M": use "b_" and 8 lowercase ASCII letters or digits`,
		},
		{name: "short ID", in: `[{"type":"paragraph","id":"b_9x3v"}]`, paths: []string{"/0/id"}, msg: `/0/id: invalid block ID "b_9x3v"`},
		{
			name:  "duplicate ID",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m"},{"type":"separator","id":"b_9x3v7k5m"}]`,
			paths: []string{"/1/id"},
			msg:   `/1/id: duplicate block ID "b_9x3v7k5m" (first used at /0/id)`,
		},
		{
			name:  "duplicate ID in content",
			in:    `[{"type":"group","id":"b_9x3v7k5m","content":[{"type":"paragraph","id":"b_9x3v7k5m"}]}]`,
			paths: []string{"/0/content/0/id"},
			msg:   `/0/content/0/id: duplicate block ID "b_9x3v7k5m" (first used at /0/id)`,
		},
		{
			name:  "ID used three times",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m"},{"type":"paragraph","id":"b_9x3v7k5m"},{"type":"paragraph","id":"b_9x3v7k5m"}]`,
			paths: []string{"/1/id", "/2/id"},
			msg:   `/2/id: duplicate block ID "b_9x3v7k5m" (first used at /0/id)`,
		},

		{
			name:  "invalid anchor",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m","anchor":"a b"}]`,
			paths: []string{"/0/anchor"},
			msg:   `/0/anchor: invalid name "a b"`,
		},
		{
			name: "duplicate anchor",
			in: `[{"type":"paragraph","id":"b_9x3v7k5m","anchor":"contact"},` +
				`{"type":"buttons","id":"b_0buttons","content":[{"type":"button","id":"b_0button1","anchor":"contact","attrs":{"label":"Go"}}]}]`,
			paths: []string{"/1/content/0/anchor"},
			msg:   `/1/content/0/anchor: duplicate anchor "contact" (first used at /0/anchor)`,
		},
		{
			name:  "invalid variant",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m","variant":"a b"}]`,
			paths: []string{"/0/variant"},
			msg:   `/0/variant: invalid name "a b"`,
		},
		{
			name:  "invalid className",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m","className":"a.b"}]`,
			paths: []string{"/0/className"},
			msg:   `/0/className: invalid name "a.b"`,
		},
		{
			name: "reserved className of a button",
			in: `[{"type":"buttons","id":"b_0buttons","content":[` +
				`{"type":"button","id":"b_0button1","className":"b-0button1","attrs":{"label":"Go"}}]}]`,
			paths: []string{"/0/content/0/className"},
			msg:   `/0/content/0/className: reserved className "b-0button1"`,
		},
		{
			name:  "blockGap of a paragraph",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m","style":{"blockGap":{"value":"1rem"}}}]`,
			paths: []string{"/0/style/blockGap"},
			msg:   "/0/style/blockGap: blockGap is not allowed for paragraph blocks",
		},
		{
			name:  "invalid style",
			in:    `[{"type":"paragraph","id":"b_9x3v7k5m","style":{"textColor":{"value":"red"},"padding":{"top":{"value":"1"}}}}]`,
			paths: []string{"/0/style/textColor/value", "/0/style/padding/top/value"},
			msg:   `/0/style/textColor/value: invalid color "red"`,
		},
		{
			name:  "invalid style of a column",
			in:    `[{"type":"columns","id":"b_0columns","content":[{"type":"column","id":"b_0column1","style":{"fontSize":{"value":"big"}}}]}]`,
			paths: []string{"/0/content/0/style/fontSize/value"},
			msg:   `/0/content/0/style/fontSize/value: invalid length "big"`,
		},

		{
			name:  "body row after a foot row",
			in:    tableJSON("head", "foot", "body"),
			paths: []string{"/0/content/2"},
			msg:   "/0/content/2: table rows must be in the order head, body, foot",
		},
		{
			name:  "rows after a foot row",
			in:    tableJSON("foot", "", "body"),
			paths: []string{"/0/content/1", "/0/content/2"},
			msg:   "/0/content/2: table rows must be in the order head, body, foot",
		},
		{
			name:  "head row after a body row",
			in:    tableJSON("", "head"),
			paths: []string{"/0/content/1"},
			msg:   "/0/content/1: table rows must be in the order head, body, foot",
		},
		{
			name:  "unknown section not ordered",
			in:    tableJSON("foot", "middle"),
			paths: []string{"/0/content/1/attrs/section"},
			msg:   `/0/content/1/attrs/section: unknown section "middle"`,
		},
		{
			name: "paragraph between head rows",
			in: `[{"type":"table","id":"b_000table","content":[` +
				`{"type":"tableRow","attrs":{"section":"head"},"content":[{"type":"tableCell"}]},` +
				`{"type":"paragraph","id":"b_0paragr1"},` +
				`{"type":"tableRow","attrs":{"section":"head"},"content":[{"type":"tableCell"}]}]}]`,
			paths: []string{"/0/content/1"},
			msg:   "/0/content/1: table can only contain tableRow",
		},
		{
			name: "rows outside a table",
			in: `[{"type":"group","id":"b_00group1","content":[` +
				`{"type":"tableRow","attrs":{"section":"foot"},"content":[{"type":"tableCell"}]},` +
				`{"type":"tableRow","attrs":{"section":"head"},"content":[{"type":"tableCell"}]}]}]`,
			paths: []string{"/0/content/0", "/0/content/1"},
			msg:   "/0/content/1: tableRow must be inside table",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocksValidate(t, mustBlocks(t, tt.in), ValidateOptions{}, tt.paths, tt.msg)
		})
	}
}

// inList is a list that holds item.
func inList(item Block) Blocks {
	return Blocks{{Type: BlockList, ID: "b_00list01", Children: Blocks{item}}}
}

// inTable is a table with a row that holds cell.
func inTable(cell Block) Blocks {
	return Blocks{{Type: BlockTable, ID: "b_000table", Children: Blocks{{Type: BlockTableRow, Children: Blocks{cell}}}}}
}

// TestBlocksValidateGoValues checks values built in Go, some of which UnmarshalJSON rejects.
func TestBlocksValidateGoValues(t *testing.T) {
	text := RichText{{Type: InlineText, Text: "a"}}
	para := Block{Type: BlockParagraph, ID: "b_0paragr1"}
	tests := []struct {
		name  string
		in    Blocks
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "nil", in: nil},
		{name: "empty", in: Blocks{}},
		{name: "listItem without ID", in: inList(Block{Type: BlockListItem, Children: Blocks{para}})},

		{
			name:  "ID of a listItem",
			in:    inList(Block{Type: BlockListItem, ID: "b_9x3v7k5m", Children: Blocks{para}}),
			paths: []string{"/0/content/0/id"},
			msg:   "/0/content/0/id: not allowed for listItem",
		},
		{
			name:  "anchor of a listItem",
			in:    inList(Block{Type: BlockListItem, Anchor: "first", Children: Blocks{para}}),
			paths: []string{"/0/content/0/anchor"},
			msg:   "/0/content/0/anchor: not allowed for listItem",
		},
		{
			name: "variant of a tableRow",
			in: Blocks{{Type: BlockTable, ID: "b_000table", Children: Blocks{
				{Type: BlockTableRow, Variant: "striped", Children: Blocks{{Type: BlockTableCell}}},
			}}},
			paths: []string{"/0/content/0/variant"},
			msg:   "/0/content/0/variant: not allowed for tableRow",
		},
		{
			name:  "className of a tableCell",
			in:    inTable(Block{Type: BlockTableCell, ClassName: "total"}),
			paths: []string{"/0/content/0/content/0/className"},
			msg:   "/0/content/0/content/0/className: not allowed for tableCell",
		},
		{
			name:  "style of a tableCell",
			in:    inTable(Block{Type: BlockTableCell, Style: Style{TextColor: Color{Preset: "primary"}}}),
			paths: []string{"/0/content/0/content/0/style"},
			msg:   "/0/content/0/content/0/style: not allowed for tableCell",
		},
		{
			name: "every common key of a tableCell",
			in: inTable(Block{
				Type: BlockTableCell, ID: "bad", Variant: "a b", ClassName: "c d", Anchor: "e f", Style: Style{TextColor: Color{Value: "red"}},
			}),
			paths: []string{
				"/0/content/0/content/0/id", "/0/content/0/content/0/variant", "/0/content/0/content/0/className",
				"/0/content/0/content/0/anchor", "/0/content/0/content/0/style",
			},
			msg: "/0/content/0/content/0/style: not allowed for tableCell",
		},

		{
			name:  "level of a paragraph",
			in:    Blocks{{Type: BlockParagraph, ID: "b_0paragr1", Attrs: Attrs{Level: 2, TextAlign: "center"}}},
			paths: []string{"/0/attrs/level"},
			msg:   "/0/attrs/level: not allowed for paragraph",
		},
		{
			name:  "attributes of a separator",
			in:    Blocks{{Type: BlockSeparator, ID: "b_separat1", Attrs: Attrs{Label: "a", Height: Length{Preset: "large"}}}},
			paths: []string{"/0/attrs/label", "/0/attrs/height"},
			msg:   "/0/attrs/height: not allowed for separator",
		},
		{
			name:  "section of a tableCell",
			in:    inTable(Block{Type: BlockTableCell, Attrs: Attrs{Section: "head", Header: true}}),
			paths: []string{"/0/content/0/content/0/attrs/section"},
			msg:   "/0/content/0/content/0/attrs/section: not allowed for tableCell",
		},
		{
			name:  "attributes of other types before the rules of the type",
			in:    Blocks{{Type: BlockHeading, ID: "b_0heading", Attrs: Attrs{Level: 7, Reason: "a"}, Text: text}},
			paths: []string{"/0/attrs/reason", "/0/attrs/level"},
			msg:   "/0/attrs/reason: not allowed for heading\n/0/attrs/level: level 7 is out of range (2-6)",
		},

		{
			name: "unknown type",
			in: Blocks{{
				Type: "bogus", ID: "bad", Style: Style{TextColor: Color{Value: "red"}}, Attrs: Attrs{Level: 1}, Code: "\x00",
				Children: Blocks{{Type: BlockListItem, ID: "bad"}},
			}},
			paths: []string{"/0/type"},
			msg:   `/0/type: unknown block type "bogus"`,
		},
		{name: "empty type", in: Blocks{{ID: "b_0paragr1"}}, paths: []string{"/0/type"}, msg: `/0/type: unknown block type ""`},
		{
			name:  "unknown type in a group",
			in:    Blocks{{Type: BlockGroup, ID: "b_00group1", Children: Blocks{{Type: "bogus"}, {Type: BlockParagraph}}}},
			paths: []string{"/0/content/0/type", "/0/content/1/id"},
			msg:   `/0/content/0/type: unknown block type "bogus"`,
		},

		{
			name:  "rich text content of an image",
			in:    Blocks{{Type: BlockImage, ID: "b_00image1", Attrs: Attrs{Media: "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", Alt: "Office"}, Text: text}},
			paths: []string{"/0/content"},
			msg:   "/0/content: rich text content is not allowed for image",
		},
		{
			name:  "child blocks of a paragraph",
			in:    Blocks{{Type: BlockParagraph, ID: "b_0paragr1", Children: Blocks{{Type: "bogus"}}}},
			paths: []string{"/0/content"},
			msg:   "/0/content: child blocks are not allowed for paragraph",
		},
		{
			name:  "text content of a paragraph",
			in:    Blocks{{Type: BlockParagraph, ID: "b_0paragr1", Code: "x"}},
			paths: []string{"/0/content"},
			msg:   "/0/content: text content is not allowed for paragraph",
		},
		{
			name:  "rich text and text content of a group",
			in:    Blocks{{Type: BlockGroup, ID: "b_00group1", Text: text, Code: "x", Children: Blocks{{Type: BlockParagraph}}}},
			paths: []string{"/0/content", "/0/content", "/0/content/0/id"},
			msg:   "/0/content: rich text content is not allowed for group\n/0/content: text content is not allowed for group",
		},
		{
			name:  "rich text and child blocks of code",
			in:    Blocks{{Type: BlockCode, ID: "b_0000code", Text: text, Children: Blocks{para}, Code: "x"}},
			paths: []string{"/0/content", "/0/content"},
			msg:   "/0/content: rich text content is not allowed for code\n/0/content: child blocks are not allowed for code",
		},
		{
			name:  "NFD in code",
			in:    Blocks{{Type: BlockCode, ID: "b_0000code", Code: "cafe" + acute}},
			paths: []string{"/0/content"},
			msg:   "/0/content: not in Unicode NFC; normalize it first",
		},
		{
			name:  "too long code",
			in:    Blocks{{Type: BlockCode, ID: "b_0000code", Code: strings.Repeat("a", MaxTextBytes+1)}},
			paths: []string{"/0/content"},
			msg:   "/0/content: too long",
		},
		{
			name:  "empty text in a heading",
			in:    Blocks{{Type: BlockHeading, ID: "b_0heading", Attrs: Attrs{Level: 2}, Text: RichText{{Type: InlineText}}}},
			paths: []string{"/0/content/0/text"},
			msg:   "/0/content/0/text: empty text",
		},
		{
			name:  "empty rich text of a heading",
			in:    Blocks{{Type: BlockHeading, ID: "b_0heading", Attrs: Attrs{Level: 2}, Text: RichText{}}},
			paths: []string{"/0/content"},
			msg:   "/0/content: empty heading",
		},
		{
			name:  "empty children of a list",
			in:    Blocks{{Type: BlockList, ID: "b_00list01", Children: Blocks{}}},
			paths: []string{"/0/content"},
			msg:   "/0/content: list needs at least one listItem",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocksValidate(t, tt.in, ValidateOptions{}, tt.paths, tt.msg)
		})
	}
}

func TestValidateClassName(t *testing.T) {
	const reserved = ": the core gives classes of the forms b-xxxxxxxx and hl-xxxxxxxx"
	tests := []struct {
		in   string
		want string // the text of the error, "" for none
	}{
		{in: "intro"},
		{in: "alignwide"},
		{in: "is-layout-row"},
		{in: "b-abc"},
		{in: "b-0paragr"},
		{in: "b-0paragr12"},
		{in: "B-0paragr1"},
		{in: "b-0PARAGR1"},
		{in: "b-0parag-1"},
		{in: "b-0parag_1"},
		{in: "b_0paragr1"},
		{in: "xb-0paragr1"},
		{in: "hl-0123456g"},
		{in: "hl-0123ABCD"},
		{in: "hl-0123456"},
		{in: "hl-012345678"},
		{in: "HL-0123abcd"},
		{in: "hl_0123abcd"},

		{in: "b-0paragr1", want: `/0/className: reserved className "b-0paragr1"` + reserved},
		{in: "b-00000000", want: `/0/className: reserved className "b-00000000"` + reserved},
		{in: "b-zzzzzzzz", want: `/0/className: reserved className "b-zzzzzzzz"` + reserved},
		{in: "hl-0123abcd", want: `/0/className: reserved className "hl-0123abcd"` + reserved},
		{in: "hl-ffffffff", want: `/0/className: reserved className "hl-ffffffff"` + reserved},
		{in: "b-0paragr1 x", want: `/0/className: invalid name "b-0paragr1 x": use ASCII letters, digits, "-" and "_"`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			bs := Blocks{{Type: BlockParagraph, ID: "b_0paragr1", ClassName: tt.in}}
			if got := errorMsg(bs.Validate(ValidateOptions{})); got != tt.want {
				t.Errorf("Validate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAllowsBlockGap(t *testing.T) {
	allowed := []BlockType{BlockList, BlockQuote, BlockButtons, BlockColumns, BlockColumn, BlockGroup}
	for typ := range blockSpecs {
		if got, want := typ.AllowsBlockGap(), slices.Contains(allowed, typ); got != want {
			t.Errorf("AllowsBlockGap() of %s = %t, want %t", typ, got, want)
		}
	}
	if BlockType("bogus").AllowsBlockGap() {
		t.Error(`AllowsBlockGap() of "bogus" = true, want false`)
	}
}

func TestValidateBlockGap(t *testing.T) {
	const media = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
	text := RichText{{Type: InlineText, Text: "a"}}
	para := Block{Type: BlockParagraph, ID: "b_0paragr1"}
	// withGap is b with a blockGap as its style.
	withGap := func(b Block) Block {
		b.Style = Style{BlockGap: Length{Preset: "small"}}
		return b
	}
	// AllowHTML and Import accept the html and legacy blocks.
	opts := ValidateOptions{AllowHTML: true, Import: true}

	// others are blocks of the types that cannot have blockGap, apart from button and the parts.
	others := Blocks{
		para,
		{Type: BlockHeading, ID: "b_0heading", Attrs: Attrs{Level: 2}, Text: text},
		{Type: BlockImage, ID: "b_000image", Attrs: Attrs{Media: media, Decorative: true}},
		{Type: BlockTable, ID: "b_000table", Children: Blocks{{Type: BlockTableRow, Children: Blocks{{Type: BlockTableCell}}}}},
		{Type: BlockSeparator, ID: "b_separat1"},
		{Type: BlockSpacer, ID: "b_00spacer", Attrs: Attrs{Height: Length{Preset: "large"}}},
		{Type: BlockCode, ID: "b_0000code"},
		{Type: BlockEmbed, ID: "b_000embed", Attrs: Attrs{Provider: "youtube", URL: "https://youtu.be/dQw4w9WgXcQ"}},
		{Type: BlockFile, ID: "b_0000file", Attrs: Attrs{Media: media}},
		{Type: BlockForm, ID: "b_0000form", Attrs: Attrs{Form: "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"}},
		{Type: BlockHTML, ID: "b_0000html", Attrs: Attrs{HTML: "<p>a</p>"}},
		{Type: BlockLegacy, ID: "b_00legacy", Attrs: Attrs{HTML: "[gallery]"}},
	}
	for _, b := range others {
		t.Run(string(b.Type), func(t *testing.T) {
			assertBlocksValidate(t, Blocks{b}, opts, nil, "")
			want := "/0/style/blockGap: blockGap is not allowed for " + string(b.Type) + " blocks"
			assertBlocksValidate(t, Blocks{withGap(b)}, opts, []string{"/0/style/blockGap"}, want)
		})
	}

	tests := []struct {
		name  string
		in    Blocks
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{
			name: "list",
			in:   Blocks{withGap(Block{Type: BlockList, ID: "b_00list01", Children: Blocks{{Type: BlockListItem, Children: Blocks{para}}}})},
		},
		{name: "quote", in: Blocks{withGap(Block{Type: BlockQuote, ID: "b_000quote", Children: Blocks{para}})}},
		{
			name: "buttons",
			in: Blocks{withGap(Block{
				Type: BlockButtons, ID: "b_0buttons", Children: Blocks{{Type: BlockButton, ID: "b_0button1", Attrs: Attrs{Label: "Go"}}},
			})},
		},
		{
			name: "columns and column",
			in:   Blocks{withGap(Block{Type: BlockColumns, ID: "b_0columns", Children: Blocks{withGap(Block{Type: BlockColumn, ID: "b_0column1"})}})},
		},
		{name: "group", in: Blocks{withGap(Block{Type: BlockGroup, ID: "b_00group1"})}},
		{
			name: "group with a layout",
			in: Blocks{{
				Type: BlockGroup, ID: "b_00group1", Style: Style{BlockGap: Length{Value: "2rem"}}, Attrs: Attrs{Layout: Layout{Type: "grid"}},
			}},
		},

		{
			name: "button",
			in: Blocks{{
				Type: BlockButtons, ID: "b_0buttons", Children: Blocks{withGap(Block{Type: BlockButton, ID: "b_0button1", Attrs: Attrs{Label: "Go"}})},
			}},
			paths: []string{"/0/content/0/style/blockGap"},
			msg:   "/0/content/0/style/blockGap: blockGap is not allowed for button blocks",
		},
		{
			name: "after the other problems of the style and before those of the attrs",
			in: Blocks{{
				Type:  BlockParagraph,
				ID:    "b_0paragr1",
				Style: Style{BlockGap: Length{Value: "1"}, Border: Border{Width: Length{Value: "10%"}}},
				Attrs: Attrs{TextAlign: "middle"},
			}},
			paths: []string{"/0/style/blockGap/value", "/0/style/border/width/value", "/0/style/blockGap", "/0/attrs/textAlign"},
			msg: `/0/style/blockGap/value: invalid length "1"` + "\n" +
				`/0/style/border/width/value: percentage "10%" is not allowed here` + "\n" +
				"/0/style/blockGap: blockGap is not allowed for paragraph blocks\n" +
				`/0/attrs/textAlign: unknown textAlign "middle"`,
		},
		{
			name:  "style of a part",
			in:    inList(withGap(Block{Type: BlockListItem, Children: Blocks{para}})),
			paths: []string{"/0/content/0/style"},
			msg:   "/0/content/0/style: not allowed for listItem",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocksValidate(t, tt.in, opts, tt.paths, tt.msg)
		})
	}
}

func TestBlocksValidateLimits(t *testing.T) {
	const (
		tooDeep = "blocks are nested too deeply (max 32)"
		tooMany = "too many blocks (max 10000)"
	)
	// nestedGroups is n groups, each in the one before, with inner in the last.
	nestedGroups := func(n int, inner Block) Blocks {
		b := inner
		for i := n - 1; i >= 0; i-- {
			b = Block{Type: BlockGroup, ID: testID(i), Children: Blocks{b}}
		}
		return Blocks{b}
	}
	// listItems is a list of n listItems, each with a paragraph.
	listItems := func(n int) Blocks {
		items := make(Blocks, n)
		for i := range items {
			items[i] = Block{Type: BlockListItem, Children: Blocks{{Type: BlockParagraph, ID: testID(i + 1)}}}
		}
		return Blocks{{Type: BlockList, ID: testID(0), Children: items}}
	}
	cells := func(n int) Blocks { return slices.Repeat(Blocks{{Type: BlockTableCell}}, n) }
	row := func(n int) Block { return Block{Type: BlockTableRow, Children: cells(n)} }
	table := func(children ...Block) Blocks { return Blocks{{Type: BlockTable, ID: testID(0), Children: children}} }
	tests := []struct {
		name  string
		in    func() Blocks
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "most blocks", in: func() Blocks { return separatorBlocks(0, MaxBlocks) }},
		{
			name:  "too many blocks",
			in:    func() Blocks { return separatorBlocks(0, MaxBlocks+1) },
			paths: []string{"/10000"},
			msg:   "/10000: " + tooMany,
		},
		{
			name:  "more blocks",
			in:    func() Blocks { return separatorBlocks(0, MaxBlocks+2) },
			paths: []string{"/10000"},
			msg:   "/10000: " + tooMany,
		},
		{
			name:  "too many blocks in a list",
			in:    func() Blocks { return listItems(5000) },
			paths: []string{"/0/content/4999/content/0"},
			msg:   "/0/content/4999/content/0: " + tooMany,
		},
		{
			name:  "unknown type counted",
			in:    func() Blocks { return append(separatorBlocks(0, MaxBlocks), Block{Type: "bogus"}) },
			paths: []string{"/10000", "/10000/type"},
			msg:   "/10000: " + tooMany + "\n" + `/10000/type: unknown block type "bogus"`,
		},
		{name: "deepest", in: func() Blocks { return nestedGroups(MaxDepth-1, Block{Type: BlockSeparator, ID: testID(MaxDepth)}) }},
		{
			name: "empty group at the deepest",
			in:   func() Blocks { return nestedGroups(MaxDepth-1, Block{Type: BlockGroup, ID: testID(MaxDepth)}) },
		},
		{
			// The separator without an ID is not checked.
			name:  "too deep",
			in:    func() Blocks { return nestedGroups(MaxDepth, Block{Type: BlockSeparator}) },
			paths: []string{"/0" + strings.Repeat("/content/0", MaxDepth-1) + "/content"},
			msg:   tooDeep,
		},
		{name: "most cells", in: func() Blocks { return table(row(MaxTableCells/2), row(MaxTableCells/2)) }},
		{
			name:  "too many cells",
			in:    func() Blocks { return table(row(MaxTableCells/2), row(MaxTableCells/2+1)) },
			paths: []string{"/0"},
			msg:   "/0: too many cells in the table (5001; max 5000)",
		},
		{
			name: "cells outside rows",
			in: func() Blocks {
				return table(row(MaxTableCells), Block{Type: BlockGroup, ID: testID(1), Children: cells(1)})
			},
			paths: []string{"/0/content/1", "/0/content/1/content/0"},
			msg:   "/0/content/1/content/0: tableCell must be inside tableRow",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocksValidate(t, tt.in(), ValidateOptions{}, tt.paths, tt.msg)
		})
	}
}

func TestValidateDocument(t *testing.T) {
	body := Blocks{
		{Type: BlockParagraph, ID: "b_9x3v7k5m", Anchor: "contact"},
		{Type: BlockParagraph, ID: "b_4n8c1w2e"},
	}
	field := Blocks{
		{Type: BlockGroup, ID: "b_00group1", Children: Blocks{{Type: BlockParagraph, ID: "b_9x3v7k5m"}}},
		{Type: BlockParagraph, ID: "b_2p6t8r1z", Anchor: "contact"},
	}
	want := `/fields/x/0/content/0/id: duplicate block ID "b_9x3v7k5m" (first used at /body/0/id)` + "\n" +
		`/fields/x/1/anchor: duplicate anchor "contact" (first used at /body/0/anchor)`
	tests := []struct {
		name string
		doc  *docState
	}{
		{name: "docState", doc: &docState{}},
		{name: "nil docState", doc: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := checker{doc: tt.doc}
			body.check(&c, "/body")
			field.check(&c, "/fields/x")
			if err := c.result(); err == nil || err.Error() != want {
				t.Errorf("result() = %v, want %s", err, want)
			}
		})
	}

	t.Run("blocks", func(t *testing.T) {
		c := checker{doc: &docState{}}
		separatorBlocks(0, 6000).check(&c, "/body")
		separatorBlocks(6000, MaxBlocks+1-6000).check(&c, "/fields/x")
		err := c.result()
		if got, want := errorPaths(t, err), []string{"/fields/x/4000"}; !reflect.DeepEqual(got, want) {
			t.Errorf("result() paths = %q, want %q", got, want)
		}
		assertErrorMsg(t, err, "too many blocks (max 10000)")
	})
}

func TestBlocksValidateOrder(t *testing.T) {
	text := RichText{{Type: InlineText, Text: "a"}}
	// The list, the table and the foot row come before the cells, so the head row is block MaxBlocks+1.
	footCells := MaxBlocks - 3
	bs := Blocks{
		{Type: BlockList, ID: "b_00list01", Children: Blocks{{
			Type:      BlockTable,
			Variant:   "a b",
			ClassName: "c d",
			Anchor:    "e f",
			Style:     Style{TextColor: Color{Value: "red"}},
			Attrs:     Attrs{Level: 2, Caption: RichText{{Type: InlineText}}},
			Text:      text,
			Children: Blocks{
				{Type: BlockTableRow, Attrs: Attrs{Section: "foot"}, Children: slices.Repeat(Blocks{{Type: BlockTableCell}}, footCells)},
				{
					Type:  BlockTableRow,
					ID:    "b_9x3v7k5m",
					Style: Style{TextColor: Color{Preset: "primary"}},
					Attrs: Attrs{Level: 2, Section: "head"},
					Code:  "x",
				},
			},
		}}},
		{Type: BlockColumns, ID: "b_0columns", Children: Blocks{
			{Type: BlockColumns, ID: "b_columns2", Children: Blocks{{Type: BlockColumn, ID: "b_0column1"}}},
			{Type: BlockHTML},
		}},
		{Type: "bogus", ID: "bad"},
	}
	want := []struct{ path, msg string }{
		{"/0/content/0", "list can only contain listItem"},
		{"/0/content/0", "too many cells in the table (9997; max 5000)"},
		{"/0/content/0/id", "missing block ID"},
		{"/0/content/0/variant", `invalid name "a b"`},
		{"/0/content/0/className", `invalid name "c d"`},
		{"/0/content/0/anchor", `invalid name "e f"`},
		{"/0/content/0/style/textColor/value", `invalid color "red"`},
		{"/0/content/0/attrs/level", "not allowed for table"},
		{"/0/content/0/attrs/caption/0/text", "empty text"},
		{"/0/content/0/content", "rich text content is not allowed for table"},
		{"/0/content/0/content/1", "table rows must be in the order head, body, foot"},
		{"/0/content/0/content/1", "too many blocks (max 10000)"},
		{"/0/content/0/content/1/id", "not allowed for tableRow"},
		{"/0/content/0/content/1/style", "not allowed for tableRow"},
		{"/0/content/0/content/1/attrs/level", "not allowed for tableRow"},
		{"/0/content/0/content/1/content", "text content is not allowed for tableRow"},
		{"/0/content/0/content/1/content", "tableRow needs at least one tableCell"},
		{"/1/content/0", "columns can only contain column"},
		{"/1/content/0", "columns cannot be nested inside columns"},
		{"/1/content/1", "columns can only contain column"},
		{"/1/content/1", "html blocks are not enabled"},
		{"/1/content/1/id", "missing block ID"},
		{"/1/content/1/attrs/html", "missing html"},
		{"/2/type", `unknown block type "bogus"`},
	}
	var paths []string
	for _, w := range want {
		paths = append(paths, w.path)
	}
	assertBlocksValidate(t, bs, ValidateOptions{}, paths, "")
	var es Errors
	if err := bs.Validate(ValidateOptions{}); !errors.As(err, &es) || len(es) != len(want) {
		t.Fatalf("Validate() = %v, want %d problems", err, len(want))
	}
	for i, e := range es {
		if e.Path != want[i].path || !strings.Contains(e.Msg, want[i].msg) {
			t.Errorf("problem %d = %q at %q, want %q at %q", i, e.Msg, e.Path, want[i].msg, want[i].path)
		}
	}
}

func TestValidateOptions(t *testing.T) {
	const (
		id          = "b_9x3v7k5m"
		media       = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
		notEnabled  = "html blocks are not enabled"
		notImported = "legacy blocks can only be created by import"
		missingAlt  = "missing alt; set decorative for a decorative image"
	)
	htmlBlock := Block{Type: BlockHTML, ID: id, Attrs: Attrs{HTML: "<p>a</p>"}}
	legacy := func(html, reason string) Block {
		return Block{Type: BlockLegacy, ID: id, Attrs: Attrs{HTML: html, Reason: reason}}
	}
	gallery := legacy("[gallery]", "shortcode gallery")
	image := func(a Attrs) Block { return Block{Type: BlockImage, ID: id, Attrs: a} }
	noAlt := image(Attrs{Media: media})
	withID := func(b Block, s string) Block {
		b.ID = s
		return b
	}
	group := func(b Block) Block { return Block{Type: BlockGroup, ID: "b_00group1", Children: Blocks{b}} }
	para := Block{Type: BlockParagraph, ID: "b_0paragr1"}
	previous := func(bs ...Block) ValidateOptions { return ValidateOptions{Previous: bs} }
	tests := []struct {
		name  string
		in    Blocks
		opts  ValidateOptions
		paths []string
		msg   string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "html", in: Blocks{htmlBlock}, paths: []string{"/0"}, msg: "/0: " + notEnabled},
		{name: "imported html", in: Blocks{htmlBlock}, opts: ValidateOptions{Import: true}, paths: []string{"/0"}, msg: "/0: " + notEnabled},
		{name: "html of the previous revision", in: Blocks{htmlBlock}, opts: previous(htmlBlock), paths: []string{"/0"}, msg: "/0: " + notEnabled},
		{name: "enabled html", in: Blocks{htmlBlock}, opts: ValidateOptions{AllowHTML: true}},

		{name: "legacy", in: Blocks{gallery}, paths: []string{"/0"}, msg: "/0: " + notImported},
		{name: "imported legacy", in: Blocks{gallery}, opts: ValidateOptions{Import: true}},
		{name: "unchanged legacy", in: Blocks{gallery}, opts: previous(gallery)},
		{name: "legacy that was in a group", in: Blocks{gallery}, opts: previous(group(gallery))},
		{name: "moved legacy", in: Blocks{para, group(gallery)}, opts: previous(gallery, para)},
		{
			name:  "legacy with other html",
			in:    Blocks{legacy(`[gallery ids="1"]`, "shortcode gallery")},
			opts:  previous(gallery),
			paths: []string{"/0"},
			msg:   "/0: " + notImported,
		},
		{
			name:  "legacy with another reason",
			in:    Blocks{legacy("[gallery]", "gallery")},
			opts:  previous(gallery),
			paths: []string{"/0"},
			msg:   "/0: " + notImported,
		},
		{
			name:  "legacy that was an html block",
			in:    Blocks{legacy("<p>a</p>", "")},
			opts:  previous(htmlBlock),
			paths: []string{"/0"},
			msg:   "/0: " + notImported,
		},
		{
			name:  "legacy not in the previous revision",
			in:    Blocks{gallery},
			opts:  previous(withID(gallery, "b_4n8c1w2e")),
			paths: []string{"/0"},
			msg:   "/0: " + notImported,
		},
		{
			name:  "legacy compared with the first block of its ID",
			in:    Blocks{gallery},
			opts:  previous(group(legacy("[gallery]", "")), gallery),
			paths: []string{"/0"},
			msg:   "/0: " + notImported,
		},

		{name: "imported image without alt", in: Blocks{noAlt}, opts: ValidateOptions{Import: true}},
		{name: "image kept without alt", in: Blocks{noAlt}, opts: previous(noAlt)},
		{name: "resized image kept without alt", in: Blocks{image(Attrs{Media: media, Width: "50%"})}, opts: previous(noAlt)},
		{
			name:  "image that had alt",
			in:    Blocks{noAlt},
			opts:  previous(image(Attrs{Media: media, Alt: "Office"})),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: " + missingAlt,
		},
		{
			name:  "image with other media",
			in:    Blocks{image(Attrs{Media: "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9"})},
			opts:  previous(noAlt),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: " + missingAlt,
		},
		{
			name:  "image that was decorative",
			in:    Blocks{noAlt},
			opts:  previous(image(Attrs{Media: media, Decorative: true})),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: " + missingAlt,
		},
		{
			name:  "image that was a file",
			in:    Blocks{noAlt},
			opts:  previous(Block{Type: BlockFile, ID: id, Attrs: Attrs{Media: media}}),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: " + missingAlt,
		},
		{
			name:  "image not in the previous revision",
			in:    Blocks{noAlt},
			opts:  previous(withID(noAlt, "b_4n8c1w2e")),
			paths: []string{"/0/attrs/alt"},
			msg:   "/0/attrs/alt: " + missingAlt,
		},
		{
			name:  "image without an ID",
			in:    Blocks{withID(noAlt, "")},
			opts:  previous(withID(noAlt, "")),
			paths: []string{"/0/id", "/0/attrs/alt"},
			msg:   "/0/attrs/alt: " + missingAlt,
		},

		{
			name:  "every block",
			in:    mustBlocks(t, readTestdata(t, "every-block.json")),
			paths: []string{"/21", "/22"},
			msg:   "/21: " + notEnabled + "\n/22: " + notImported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertBlocksValidate(t, tt.in, tt.opts, tt.paths, tt.msg)
		})
	}
}

func TestCheckerRef(t *testing.T) {
	type call struct {
		kind RefKind
		id   string
	}
	const (
		image   = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
		form    = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"
		invalid = `/x: invalid entry ID "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5": use "ent_" and a ULID (26 characters in upper case)`
	)
	var calls []call
	opts := ValidateOptions{Lookup: func(kind RefKind, id string) (Ref, bool) {
		calls = append(calls, call{kind, id})
		return Ref{Type: MediaImage}, id == image
	}}
	tests := []struct {
		name  string
		doc   *docState
		kind  RefKind
		id    string
		ref   Ref
		ok    bool
		calls []call // calls of Lookup
		want  string // text of the error, or "" for none
	}{
		{
			name:  "exists",
			doc:   &docState{opts: opts},
			kind:  RefMedia,
			id:    image,
			ref:   Ref{Type: MediaImage},
			ok:    true,
			calls: []call{{RefMedia, image}},
		},
		{
			name:  "does not exist",
			doc:   &docState{opts: opts},
			kind:  RefForm,
			id:    form,
			ref:   Ref{Type: MediaImage},
			calls: []call{{RefForm, form}},
			want:  `/x: form "` + form + `" does not exist`,
		},
		{name: "prefix of another kind not looked up", doc: &docState{opts: opts}, kind: RefEntry, id: image, want: invalid},
		{name: "without Lookup", doc: &docState{}, kind: RefMedia, id: image},
		{name: "outside a document", kind: RefMedia, id: image},
		{name: "invalid outside a document", kind: RefEntry, id: image, want: invalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls = nil
			c := checker{doc: tt.doc}
			ref, ok := c.ref("/x", tt.kind, tt.id)
			if ref != tt.ref || ok != tt.ok {
				t.Errorf("ref(%q, %q) = %v, %t, want %v, %t", tt.kind, tt.id, ref, ok, tt.ref, tt.ok)
			}
			got := ""
			if err := c.result(); err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("ref(%q, %q) error = %q, want %q", tt.kind, tt.id, got, tt.want)
			}
			if !reflect.DeepEqual(calls, tt.calls) {
				t.Errorf("Lookup calls = %v, want %v", calls, tt.calls)
			}
		})
	}
}

func TestCheckerMedia(t *testing.T) {
	const (
		image   = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5"
		pdf     = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7"
		unknown = "med_01JB8Z3K5SK6Y9B3E7T1A4G8H2"
		missing = "med_01JB8Z3K5XR2T5V8Y1A4C7E3G6"
	)
	media := map[string]Ref{image: {Type: MediaImage}, pdf: {Type: MediaPDF}, unknown: {}}
	opts := ValidateOptions{Lookup: func(_ RefKind, id string) (Ref, bool) {
		r, ok := media[id]
		return r, ok
	}}
	tests := []struct {
		name   string
		id     string
		accept []string
		want   string
	}{
		{name: "image as an image", id: image, accept: []string{MediaImage}},
		{name: "PDF as any medium", id: pdf},
		{name: "PDF as an image or a PDF", id: pdf, accept: []string{MediaImage, MediaPDF}},
		{name: "PDF as an image", id: pdf, accept: []string{MediaImage}, want: `/x: media "` + pdf + `" (pdf) is not accepted here (accept: image)`},
		{
			name:   "unknown kind",
			id:     unknown,
			accept: []string{MediaImage, MediaPDF},
			want:   `/x: media "` + unknown + `" (unknown kind) is not accepted here (accept: image, pdf)`,
		},
		{name: "unknown kind as any medium", id: unknown},
		{name: "missing", id: missing, accept: []string{MediaImage}, want: `/x: media "` + missing + `" does not exist`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := checker{doc: &docState{opts: opts}}
			c.media("/x", tt.id, tt.accept...)
			got := ""
			if err := c.result(); err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("media(%q, %q) = %q, want %q", tt.id, tt.accept, got, tt.want)
			}
		})
	}
}

func TestValidateRefs(t *testing.T) {
	in := mustBlocks(t, `[
  {"type": "paragraph", "id": "b_0paragr1", "content": [
    {"type": "text", "text": "a", "marks": [{"type": "link", "attrs": {"entry": "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"}}]},
    {"type": "text", "text": "b", "marks": [{"type": "link", "attrs": {"media": "med_01JB8Z3K5XR2T5V8Y1A4C7E3G6"}}]}
  ]},
  {
    "type": "image",
    "id": "b_000image",
    "attrs": {
      "media": "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5",
      "alt": "Office",
      "caption": [{"type": "text", "text": "c", "marks": [{"type": "link", "attrs": {"entry": "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9"}}]}],
      "link": {"media": "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9"}
    }
  },
  {"type": "buttons", "id": "b_0buttons", "content": [
    {"type": "button", "id": "b_0button1", "attrs": {"label": "Contact", "link": {"entry": "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8", "anchor": "form"}}},
    {"type": "button", "id": "b_0button2", "attrs": {"label": "Download", "link": {"media": "med_01JB8Z3K5SK6Y9B3E7T1A4G8H2"}}}
  ]},
  {"type": "file", "id": "b_0000file", "attrs": {"media": "med_01JB8Z3K5WN9C2E6H1K4P7S3T8"}},
  {"type": "form", "id": "b_0000form", "attrs": {"form": "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"}}
]`)
	missing := []string{"med_01JB8Z3K5XR2T5V8Y1A4C7E3G6", "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9", "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8"}
	var calls []string
	opts := ValidateOptions{Lookup: func(kind RefKind, id string) (Ref, bool) {
		calls = append(calls, string(kind)+" "+id)
		return Ref{Type: MediaImage}, !slices.Contains(missing, id)
	}}
	err := in.Validate(opts)
	wantCalls := []string{
		"entry ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3", "media med_01JB8Z3K5XR2T5V8Y1A4C7E3G6",
		"media med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", "entry ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9", "media med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9",
		"entry ent_01JB8Y6T2WT6X9C3E6G9J2K5M8", "media med_01JB8Z3K5SK6Y9B3E7T1A4G8H2",
		"media med_01JB8Z3K5WN9C2E6H1K4P7S3T8",
		"form frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("Lookup calls = %q, want %q", calls, wantCalls)
	}
	want := `/0/content/1/marks/0/attrs/media: media "med_01JB8Z3K5XR2T5V8Y1A4C7E3G6" does not exist` + "\n" +
		`/1/attrs/caption/0/marks/0/attrs/entry: entry "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9" does not exist` + "\n" +
		`/2/content/0/attrs/link/entry: entry "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" does not exist`
	if err == nil || err.Error() != want {
		t.Errorf("Validate() = %v, want %s", err, want)
	}
	if err := in.Validate(ValidateOptions{}); err != nil {
		t.Errorf("Validate() without Lookup = %v, want nil", err)
	}
}
