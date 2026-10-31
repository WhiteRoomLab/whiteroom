package yaml

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// worksSchema is the example of site/schema.yaml in 6.4.4 of the concept (docs/06.04-entries.md).
const worksSchema = `# 例：site/schema.yaml（スキーマのファイル。6.6.8）
schemaVersion: 1
types:
  works:
    label: 制作実績
    url: /works/{slug}/
    archive: /works/      # 一覧のパス（6.6.8）
    taxonomies: [works_category]
    body: true            # 本文（ブロック）を使う
    fields:
      client:
        type: text
        label: クライアント名
        required: true
        maxLength: 100
      year:
        type: number
        label: 制作年
        min: 1990
        max: 2100
        translatable: false   # 翻訳グループ内で同じ値に保つ
      images:
        type: list
        label: 画像
        of: { type: media, accept: [image] }
        maxItems: 20

taxonomies:
  works_category:
    label: 実績の種類
    hierarchical: false
`

// nested returns n block mappings, each the value of the key "a" of the one before, and the JSON of them.
func nested(n int) (string, string) {
	var b strings.Builder
	for i := range n {
		b.WriteString(strings.Repeat(" ", i) + "a:\n")
	}
	b.WriteString(strings.Repeat(" ", n) + "1\n")
	return b.String(), strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n)
}

var (
	nested100, nested100JSON = nested(100)
	nested101, _             = nested(101)
)

var toJSONTests = []struct {
	name string
	in   string
	want string
	pos  map[string]Position
}{
	{
		name: "block mapping",
		in:   "a: 1\nb: x\n",
		want: `{"a":1,"b":"x"}`,
		pos:  map[string]Position{"": {1, 1}, "/a": {1, 1}, "/b": {2, 1}},
	},
	{
		name: "block sequence",
		in:   "- a\n- b\n",
		want: `["a","b"]`,
		pos:  map[string]Position{"": {1, 1}, "/0": {1, 3}, "/1": {2, 3}},
	},
	{
		name: "nested mappings",
		in:   "a:\n  b: 1\n  c:\n    d: x\ne: y\n",
		want: `{"a":{"b":1,"c":{"d":"x"}},"e":"y"}`,
		pos:  map[string]Position{"/a": {1, 1}, "/a/b": {2, 3}, "/a/c": {3, 3}, "/a/c/d": {4, 5}, "/e": {5, 1}},
	},
	{
		name: "sequence in a mapping",
		in:   "a:\n  - x\n  - y\nb: 1\n",
		want: `{"a":["x","y"],"b":1}`,
		pos:  map[string]Position{"/a/0": {2, 5}, "/a/1": {3, 5}, "/b": {4, 1}},
	},
	{
		name: "sequence at the indentation of its key",
		in:   "a:\n- x\n- y\nb: 1\n",
		want: `{"a":["x","y"],"b":1}`,
		pos:  map[string]Position{"/a": {1, 1}, "/a/0": {2, 3}, "/a/1": {3, 3}, "/b": {4, 1}},
	},
	{
		name: "compact mappings",
		in:   "- a: 1\n  b: 2\n- c: 3\n",
		want: `[{"a":1,"b":2},{"c":3}]`,
		pos:  map[string]Position{"/0": {1, 3}, "/0/a": {1, 3}, "/0/b": {2, 3}, "/1": {3, 3}, "/1/c": {3, 3}},
	},
	{
		name: "compact sequences",
		in:   "- - a\n  - b\n- c\n",
		want: `[["a","b"],"c"]`,
		pos:  map[string]Position{"/0": {1, 3}, "/0/0": {1, 5}, "/0/1": {2, 5}, "/1": {3, 3}},
	},
	{
		name: "sequence at the indentation of its key in a compact mapping",
		in:   "- a:\n  - x\n  b:\n    c: 1\n- d\n",
		want: `[{"a":["x"],"b":{"c":1}},"d"]`,
		pos:  map[string]Position{"/0/a/0": {2, 5}, "/0/b": {3, 3}, "/0/b/c": {4, 5}, "/1": {5, 3}},
	},
	{
		name: "compact nodes in deeper compact nodes",
		in:   "- - a: 1\n    b:\n    - c\n",
		want: `[[{"a":1,"b":["c"]}]]`,
		pos:  map[string]Position{"/0": {1, 3}, "/0/0": {1, 5}, "/0/0/b": {2, 5}, "/0/0/b/0": {3, 7}},
	},
	{
		name: "item one space deeper than its dash",
		in:   "-\n x\n",
		want: `["x"]`,
		pos:  map[string]Position{"/0": {2, 2}},
	},
	{
		name: "flow indicators in block items",
		in:   "- a,b\n- c]d\n",
		want: `["a,b","c]d"]`,
	},
	{
		name: "scalar on the next line",
		in:   "a:\n  x\nb:\n    'y' # c\n",
		want: `{"a":"x","b":"y"}`,
		pos:  map[string]Position{"/a": {1, 1}, "/b": {3, 1}},
	},
	{
		name: "flow on the next line",
		in:   "a:\n  [x, y]\n",
		want: `{"a":["x","y"]}`,
		pos:  map[string]Position{"/a": {1, 1}, "/a/0": {2, 4}, "/a/1": {2, 7}},
	},
	{
		name: "item on the next lines",
		in:   "-\n  a: 1\n  b: 2\n- # c\n  - x\n",
		want: `[{"a":1,"b":2},["x"]]`,
		pos:  map[string]Position{"": {1, 1}, "/0": {2, 3}, "/0/b": {3, 3}, "/1": {5, 3}},
	},
	{
		name: "spaces before the colon",
		in:   "a : 1\n\"b\"  : 2\n'c' :\n  3\n",
		want: `{"a":1,"b":2,"c":3}`,
		pos:  map[string]Position{"/b": {2, 1}, "/c": {3, 1}},
	},
	{
		name: "comments",
		in:   "# head\na: 1 # after\n  # indented\n# at the start\nb: C#\nc: Rock #1\nd: # before the value\n  x\ne: [f, # in a flow\n  g]   #\n#",
		want: `{"a":1,"b":"C#","c":"Rock","d":"x","e":["f","g"]}`,
		pos:  map[string]Position{"": {2, 1}, "/d": {7, 1}, "/e/1": {10, 3}},
	},
	{
		name: "quoted value that starts with #",
		in:   "primary: \"#1e73be\"\n",
		want: `{"primary":"#1e73be"}`,
	},
	{
		name: "comment after a key with a nested mapping",
		in:   "a: # comment\n  b: 1\n",
		want: `{"a":{"b":1}}`,
		pos:  map[string]Position{"/a": {1, 1}, "/a/b": {2, 3}},
	},
	{
		name: "comment after a value",
		in:   "a: x # comment\n",
		want: `{"a":"x"}`,
	},
	{
		name: "comment after a key in a flow mapping",
		in:   "a: {b: # comment\n  c}\n",
		want: `{"a":{"b":"c"}}`,
		pos:  map[string]Position{"/a/b": {1, 5}},
	},
	{
		name: "document start",
		in:   "# comment\n\n--- # start\na: 1\n",
		want: `{"a":1}`,
		pos:  map[string]Position{"": {4, 1}, "/a": {4, 1}},
	},
	{
		name: "byte order mark",
		in:   "\xef\xbb\xbfa: 1\n",
		want: `{"a":1}`,
		pos:  map[string]Position{"": {1, 1}, "/a": {1, 1}},
	},
	{
		name: "CRLF",
		in:   "a: 1\r\nb:\r\n  - x\r\n\r\nc: 'y'\r\n",
		want: `{"a":1,"b":["x"],"c":"y"}`,
		pos:  map[string]Position{"/b/0": {3, 5}, "/c": {5, 1}},
	},
	{
		name: "no line break at the end",
		in:   "a: 1",
		want: `{"a":1}`,
	},
	{
		name: "trailing spaces and blank lines",
		in:   "a: 1  \n   \n\nb: [x]  \n  ",
		want: `{"a":1,"b":["x"]}`,
	},
	{
		name: "flow collections",
		in:   "a: [x, {b: c, d: [1, 2]}, [], {}]\n",
		want: `{"a":["x",{"b":"c","d":[1,2]},[],{}]}`,
		pos: map[string]Position{
			"/a/0": {1, 5}, "/a/1": {1, 8}, "/a/1/b": {1, 9}, "/a/1/d": {1, 15}, "/a/1/d/1": {1, 22},
			"/a/2": {1, 27}, "/a/3": {1, 31},
		},
	},
	{
		name: "multi-line flow collections",
		in:   "a: [x,\n  # comment\n\n  {b: c},\n y, ]\nd: {\n  e: 1, f:\n    2\n  }\n",
		want: `{"a":["x",{"b":"c"},"y"],"d":{"e":1,"f":2}}`,
		pos:  map[string]Position{"/a/1": {4, 3}, "/a/1/b": {4, 4}, "/a/2": {5, 2}, "/d/f": {7, 9}},
	},
	{
		name: "comment lines at the start of a line in a flow",
		in:   "a: [x,\n# c\n  y]\nb: [z\n# c\n  ]\n",
		want: `{"a":["x","y"],"b":["z"]}`,
	},
	{
		name: "trailing commas",
		in:   "a: [x, y, ]\nb: {c: 1,}\n",
		want: `{"a":["x","y"],"b":{"c":1}}`,
	},
	{
		name: "empty flow collections",
		in:   "a: []\nb: {}\nc: [ ]\nd: {\n  }\n",
		want: `{"a":[],"b":{},"c":[],"d":{}}`,
	},
	{
		name: "JSON",
		in:   `{"a":"b","c":[1,-2.5e3,true,false],"d":{}}`,
		want: `{"a":"b","c":[1,-2.5e3,true,false],"d":{}}`,
		pos:  map[string]Position{"": {1, 1}, "/a": {1, 2}, "/c": {1, 10}, "/c/1": {1, 17}, "/d": {1, 36}},
	},
	{
		name: "keys in flow mappings",
		in:   `{"a":b, 'c':d, e: f, g:h: i, "j" : k, l:m: n}` + "\n",
		want: `{"a":"b","c":"d","e":"f","g:h":"i","j":"k","l:m":"n"}`,
	},
	{
		name: "colon on the line after a key in a flow mapping",
		in:   "{a\n: b, \"c\"\n:d, e # comment\n: f}",
		want: `{"a":"b","c":"d","e":"f"}`,
		pos:  map[string]Position{"/a": {1, 2}, "/c": {2, 6}, "/e": {3, 5}},
	},
	{
		name: "colon alone on the line after a key in a flow mapping",
		in:   "{a\n:\n  b}",
		want: `{"a":"b"}`,
	},
	{
		name: "plain scalars in flow collections",
		in:   `[a :b, ::c, -1, -d, ?e, a#b, 'x' , "y" ,z]`,
		want: `["a :b","::c",-1,"-d","?e","a#b","x","y","z"]`,
	},
	{
		name: "plain scalars in block collections",
		in: "a: http://example.com/a#b\nb: a, b [c] {d}\nc: -1\nd: -foo\ne: ::x\nf: ?x\ng: x:y\nh: x]\n" +
			"i: a!b&c*d|e>f%g@h`i\n",
		want: `{"a":"http://example.com/a#b","b":"a, b [c] {d}","c":-1,"d":"-foo","e":"::x","f":"?x","g":"x:y",` +
			`"h":"x]","i":"a!b&c*d|e>f%g@h` + "`" + `i"}`,
	},
	{
		name: "colons and quotes in plain scalars",
		in:   "a: b :c\na\"b: c\n?[: d\ne: x,#y\n",
		want: `{"a":"b :c","a\"b":"c","?[":"d","e":"x,#y"}`,
	},
	{
		name: "numbers in sequences",
		in:   "- -1\n- - 1\n- [-1, -0.5]\n",
		want: `[-1,[1],[-1,-0.5]]`,
		pos:  map[string]Position{"/1": {2, 3}, "/1/0": {2, 5}, "/2/1": {3, 8}},
	},
	{
		name: "keys ending with colons",
		in:   "a::: v\nb:c: w\n-d: x\n?e: y\n",
		want: `{"a::":"v","b:c":"w","-d":"x","?e":"y"}`,
	},
	{
		name: "single-quoted",
		in:   `['it''s', 'a\nb', '', ' # x: y ', '"']`,
		want: `["it's","a\\nb",""," # x: y ","\""]`,
	},
	{
		name: "tabs in quotes and comments",
		in:   "a: \"x\ty\" #\tc\nb: 'z\t'\n#\t\n",
		want: `{"a":"x\ty","b":"z\t"}`,
	},
	{
		name: "core schema",
		in: "a: no\nb: yes\nc: on\nd: off\ne: True\nf: FALSE\ng: 1e3\nh: -0\ni: 2.0\nj: 1_000\nk: 2026-10-30\n" +
			"l: 0b101\nm: 1:30\nn: -.nan\no: 0o\np: 0x\nq: +\nr: .\ns: 1e\nt: 0.5\nu: 12345678901234567890\n" +
			"v: -1.5E+3\nw: 1.5e-3\nx: '1'\ny: \"true\"\nz: 0O17\nA: 0X1F\nB: true\nC: false\nD: TRUE\nE: tRUE\n" +
			"F: 9\nG: 0o8\nH: 0xg1\n",
		want: `{"a":"no","b":"yes","c":"on","d":"off","e":true,"f":false,"g":1e3,"h":-0,"i":2.0,"j":"1_000",` +
			`"k":"2026-10-30","l":"0b101","m":"1:30","n":"-.nan","o":"0o","p":"0x","q":"+","r":".","s":"1e",` +
			`"t":0.5,"u":12345678901234567890,"v":-1.5E+3,"w":1.5e-3,"x":"1","y":"true","z":"0O17","A":"0X1F",` +
			`"B":true,"C":false,"D":true,"E":"tRUE","F":9,"G":"0o8","H":"0xg1"}`,
	},
	{
		name: "Japanese",
		in:   "名前: 値\nキー: [あ, い]\n表: {あ: い, う: え}\n日本語の配列:\n  - 項目 # コメント\n",
		want: `{"名前":"値","キー":["あ","い"],"表":{"あ":"い","う":"え"},"日本語の配列":["項目"]}`,
		pos: map[string]Position{
			"/名前": {1, 1}, "/キー/0": {2, 6}, "/キー/1": {2, 9}, "/表/う": {3, 11}, "/日本語の配列/0": {5, 5},
		},
	},
	{
		name: "characters that are not spaces",
		in:   "a: x" + string(rune(0x85)) + "y" + string(rune(0x3000)) + "\n" + string(rune(0x3000)) + ": " + string(rune(0x2028)) + "\n",
		want: `{"a":"x` + string(rune(0x85)) + "y" + string(rune(0x3000)) + `","` + string(rune(0x3000)) + `":"` + string(rune(0x2028)) + `"}`,
	},
	{
		name: "edges of the allowed characters",
		in:   "a: " + string([]rune{0xA0, 0xD7FF, 0xE000, 0xFFFD, 0x10000, 0x10FFFF}) + "\n",
		want: `{"a":"` + string([]rune{0xA0, 0xD7FF, 0xE000, 0xFFFD, 0x10000, 0x10FFFF}) + `"}`,
	},
	{
		name: "plain scalar at the root",
		in:   "hello world # c\n",
		want: `"hello world"`,
		pos:  map[string]Position{"": {1, 1}},
	},
	{
		name: "number at the root",
		in:   "\n  42\n",
		want: `42`,
		pos:  map[string]Position{"": {2, 3}},
	},
	{
		name: "quoted scalar at the root",
		in:   `"a: b"`,
		want: `"a: b"`,
	},
	{
		name: "flow at the root",
		in:   "  [1,\n2, { a: b,\n}\n ]\n",
		want: `[1,2,{"a":"b"}]`,
		pos:  map[string]Position{"": {1, 3}, "/1": {2, 1}, "/2/a": {2, 6}},
	},
	{
		name: "block mapping at an indentation",
		in:   "  a: 1\n  b:\n  - 2\n",
		want: `{"a":1,"b":[2]}`,
		pos:  map[string]Position{"": {1, 3}, "/b/0": {3, 5}},
	},
	{
		name: "empty key",
		in:   `"": a`,
		want: `{"":"a"}`,
		pos:  map[string]Position{"/": {1, 1}},
	},
	{
		name: "keys with ~ and /",
		in:   "a~/b:\n  c/d: 1\n  '~': [x]\n",
		want: `{"a~/b":{"c/d":1,"~":["x"]}}`,
		pos:  map[string]Position{"/a~0~1b": {1, 1}, "/a~0~1b/c~1d": {2, 3}, "/a~0~1b/~0/0": {3, 9}},
	},
	{
		name: "long keys",
		in: strings.Repeat("k", 1024) + ": 1\n" + strings.Repeat("l", 1020) + "    : 2\n" +
			`"` + strings.Repeat("m", 1022) + `": 3` + "\n" + "x: {" + strings.Repeat("n", 2000) + ": 4}\n",
		want: `{"` + strings.Repeat("k", 1024) + `":1,"` + strings.Repeat("l", 1020) + `":2,"` + strings.Repeat("m", 1022) +
			`":3,"x":{"` + strings.Repeat("n", 2000) + `":4}}`,
	},
	{
		name: "document markers in values",
		in:   "a: ...\nb: --- x\nc: ---\n---x: d\n",
		want: `{"a":"...","b":"--- x","c":"---","---x":"d"}`,
	},
	{
		name: "dashes at the start of a line in a flow",
		in:   "[a,\n---, b]",
		want: `["a","---","b"]`,
	},
	{
		name: "plain scalar starting with ---",
		in:   "---a\n",
		want: `"---a"`,
	},
	{
		name: "plain scalar starting with --- after the document start",
		in:   "--- # c\n---a # c\n",
		want: `"---a"`,
	},
	{
		name: "nesting 100 deep",
		in:   nested100,
		want: nested100JSON,
		pos:  map[string]Position{"/a/a/a": {3, 3}},
	},
	{
		name: "sequences 100 deep",
		in:   strings.Repeat("- ", 100) + "a",
		want: strings.Repeat("[", 100) + `"a"` + strings.Repeat("]", 100),
	},
	{
		name: "more than 100 sibling collections",
		in:   strings.Repeat("- [x]\n- a: 1\n- - b\n", 101),
		want: "[" + strings.TrimSuffix(strings.Repeat(`["x"],{"a":1},["b"],`, 101), ",") + "]",
	},
	{
		name: "flow collections 100 deep",
		in:   "a: " + strings.Repeat("[", 99) + strings.Repeat("]", 99),
		want: `{"a":` + strings.Repeat("[", 99) + strings.Repeat("]", 99) + "}",
	},
}

func TestToJSON(t *testing.T) {
	for _, tt := range toJSONTests {
		t.Run(tt.name, func(t *testing.T) {
			out, pos, err := ToJSON([]byte(tt.in))
			if err != nil {
				t.Fatalf("ToJSON(%q) = %v", tt.in, err)
			}
			if string(out) != tt.want {
				t.Errorf("ToJSON(%q) = %s, want %s", tt.in, out, tt.want)
			}
			checkPointers(t, out, pos)
			for ptr, want := range tt.pos {
				if got, ok := pos[ptr]; !ok || got != want {
					t.Errorf("positions[%q] = %v, %t; want %v", ptr, got, ok, want)
				}
			}
		})
	}
}

func TestToJSONStrings(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: `"\0"`, want: "\x00"},
		{in: `"\a"`, want: "\a"},
		{in: `"\b"`, want: "\b"},
		{in: `"\t"`, want: "\t"},
		{in: "\"\\\t\"", want: "\t"},
		{in: `"\n"`, want: "\n"},
		{in: `"\v"`, want: "\v"},
		{in: `"\f"`, want: "\f"},
		{in: `"\r"`, want: "\r"},
		{in: `"\e"`, want: "\x1b"},
		{in: `"\ "`, want: " "},
		{in: `"\""`, want: `"`},
		{in: `"\/"`, want: "/"},
		{in: `"\\"`, want: `\`},
		{in: `"\N"`, want: string(rune(0x85))},
		{in: `"\_"`, want: string(rune(0xA0))},
		{in: `"\L"`, want: string(rune(0x2028))},
		{in: `"\P"`, want: string(rune(0x2029))},
		{in: `"\x41\x7e\xe9"`, want: "A~" + string(rune(0xE9))},
		{in: "\"\\u00e9\\u00E9\\u0000\\u3042\"", want: string([]rune{0xE9, 0xE9, 0, 0x3042})},
		{in: `"\U0001F600\U0010FFFD\U0010FFFF"`, want: string([]rune{0x1F600, 0x10FFFD, 0x10FFFF})},
		{in: `"\x4a\x4A"`, want: "JJ"},
		{in: `"\x85"`, want: string(rune(0x85))},
		{in: `"a 'b' \"c\" # d: e"`, want: `a 'b' "c" # d: e`},
		{in: `""`, want: ""},
		{in: `''`, want: ""},
		{in: `'it''s'`, want: "it's"},
		{in: `'\n'`, want: `\n`},
		{in: "\"\ta\t\"", want: "\ta\t"},
		{in: "'" + string(rune(0x3042)) + "'", want: string(rune(0x3042))},
	}
	for _, tt := range tests {
		out, _, err := ToJSON([]byte(tt.in))
		if err != nil {
			t.Errorf("ToJSON(%q) = %v", tt.in, err)
			continue
		}
		var got string
		if err := json.Unmarshal(out, &got); err != nil || got != tt.want {
			t.Errorf("ToJSON(%q) = %s, %v; want the string %+q", tt.in, out, err, tt.want)
		}
	}
}

// TestToJSONEscapesJSON checks that strings are written as jsontext writes them.
func TestToJSONEscapesJSON(t *testing.T) {
	in := `"\0\b\t\n\v\f\r\e\"\\/` + string([]rune{0x85, 0x2028}) + `"`
	want := `"` + "\\u0000" + `\b\t\n` + "\\u000b" + `\f\r` + "\\u001b" + `\"\\/` + string([]rune{0x85, 0x2028}) + `"`
	out, _, err := ToJSON([]byte(in))
	if err != nil || string(out) != want {
		t.Errorf("ToJSON(%q) = %s, %v; want %s", in, out, err, want)
	}
}

func TestToJSONWorksSchema(t *testing.T) {
	out, pos, err := ToJSON([]byte(worksSchema))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"types":{"works":{"label":"制作実績","url":"/works/{slug}/","archive":"/works/",` +
		`"taxonomies":["works_category"],"body":true,"fields":{` +
		`"client":{"type":"text","label":"クライアント名","required":true,"maxLength":100},` +
		`"year":{"type":"number","label":"制作年","min":1990,"max":2100,"translatable":false},` +
		`"images":{"type":"list","label":"画像","of":{"type":"media","accept":["image"]},"maxItems":20}}}},` +
		`"taxonomies":{"works_category":{"label":"実績の種類","hierarchical":false}}}`
	if string(out) != want {
		t.Errorf("ToJSON() = %s, want %s", out, want)
	}
	checkPointers(t, out, pos)
	for ptr, want := range map[string]Position{
		"":                                        {2, 1},
		"/schemaVersion":                          {2, 1},
		"/types":                                  {3, 1},
		"/types/works":                            {4, 3},
		"/types/works/label":                      {5, 5},
		"/types/works/archive":                    {7, 5},
		"/types/works/taxonomies/0":               {8, 18},
		"/types/works/fields/client/label":        {13, 9},
		"/types/works/fields/year/min":            {19, 9},
		"/types/works/fields/images/of":           {25, 9},
		"/types/works/fields/images/of/type":      {25, 15},
		"/types/works/fields/images/of/accept":    {25, 28},
		"/types/works/fields/images/of/accept/0":  {25, 37},
		"/types/works/fields/images/maxItems":     {26, 9},
		"/taxonomies":                             {28, 1},
		"/taxonomies/works_category/hierarchical": {31, 5},
	} {
		if got, ok := pos[ptr]; !ok || got != want {
			t.Errorf("positions[%q] = %v, %t; want %v", ptr, got, ok, want)
		}
	}
}

func TestToJSONErrors(t *testing.T) {
	const (
		indent  = "unexpected indentation"
		missing = "missing value"
	)
	tests := []struct {
		name      string
		in        string
		msg       string
		line, col int
	}{
		{name: "tab in the indentation", in: "a:\n\tb: 1\n", msg: msgTab, line: 2, col: 1},
		{name: "tab after the indentation", in: "a:\n  \tb: 1\n", msg: msgTab, line: 2, col: 3},
		{name: "tab after a colon", in: "a:\tb\n", msg: msgTab, line: 1, col: 3},
		{name: "tab after a dash", in: "-\tb\n", msg: msgTab, line: 1, col: 2},
		{name: "tab in a value", in: "a: b\tc\n", msg: msgTab, line: 1, col: 5},
		{name: "tab at the end of a line", in: "a: b\t\n", msg: msgTab, line: 1, col: 5},
		{name: "tab before a comment", in: "a: b \t# c\n", msg: msgTab, line: 1, col: 6},
		{name: "tab after a quoted value", in: "a: 'b'\t\n", msg: msgTab, line: 1, col: 7},
		{name: "tab in a blank line", in: "a: 1\n\t\nb: 2\n", msg: msgTab, line: 2, col: 1},
		{name: "tab before a comment line", in: "a: 1\n \t# c\n", msg: msgTab, line: 2, col: 2},
		{name: "tab in a flow", in: "[a,\tb]", msg: msgTab, line: 1, col: 4},
		{name: "tab in the indentation of a flow", in: "[a,\n\tb]", msg: msgTab, line: 2, col: 1},
		{name: "tab on the line after a plain scalar in a flow", in: "[a\n\tb]", msg: msgTab, line: 2, col: 1},
		{name: "tab after ---", in: "---\t\na: 1\n", msg: msgTab, line: 1, col: 4},
		{name: "tab after a key in Japanese", in: "キー: 値\t\n", msg: msgTab, line: 1, col: 6},
		{name: "CR", in: "a: 1\rb: 2\n", msg: "lone CR is not allowed; use LF or CRLF line breaks", line: 1, col: 5},
		{name: "CR at the end", in: "a: 1\n\r", msg: "lone CR is not allowed; use LF or CRLF line breaks", line: 2, col: 1},
		{name: "invalid UTF-8", in: "a: \xff\n", msg: "invalid UTF-8", line: 1, col: 4},
		{name: "truncated UTF-8", in: "あ: \xe3\x81", msg: "invalid UTF-8", line: 1, col: 4},
		{name: "encoded surrogate", in: "a: \xed\xa0\x80", msg: "invalid UTF-8", line: 1, col: 4},
		{name: "control character", in: "a: \x01\n", msg: "character U+0001 is not allowed", line: 1, col: 4},
		{name: "NUL", in: "a\x00", msg: "character U+0000 is not allowed", line: 1, col: 2},
		{name: "DEL in quotes", in: "a: \"\x7f\"\n", msg: "character U+007F is not allowed", line: 1, col: 5},
		{name: "C1 control in a comment", in: "a: 1 # \xc2\x80\n", msg: "character U+0080 is not allowed", line: 1, col: 8},
		{name: "noncharacter", in: "\na: \xef\xbf\xbf\n", msg: "character U+FFFF is not allowed", line: 2, col: 4},
		{name: "byte order mark after the start", in: "a: \xef\xbb\xbf\n", msg: "byte order mark U+FEFF", line: 1, col: 4},
		{name: "second byte order mark", in: "\xef\xbb\xbf\xef\xbb\xbfa: 1", msg: "byte order mark U+FEFF", line: 1, col: 1},
		{name: "byte order mark in quotes", in: "a: '\xef\xbb\xbf'", msg: "byte order mark U+FEFF", line: 1, col: 5},
		{name: "byte order mark at the start of a line", in: "a: 1\n\xef\xbb\xbfb: 2", msg: "byte order mark U+FEFF", line: 2, col: 1},
		{name: "directive", in: "%YAML 1.2\n---\na: 1\n", msg: "directives such as %YAML are not supported", line: 1, col: 1},
		{name: "directive after a comment", in: "# c\n%TAG ! x\n---\n", msg: "directives such as %YAML", line: 2, col: 1},
		{name: "percent sign in the document", in: "a: 1\n%b: 2\n", msg: `unquoted text cannot start with "%"`, line: 2, col: 1},
		{name: "second document", in: "a: 1\n---\nb: 2\n", msg: `multiple documents are not supported; remove this "---"`, line: 2, col: 1},
		{name: "two document starts", in: "---\n--- # c\na: 1\n", msg: "multiple documents", line: 2, col: 1},
		{name: "second document after a tab", in: "a: 1\n---\tb: 2\n", msg: "multiple documents", line: 2, col: 1},
		{name: "document end", in: "a: 1\n...\n", msg: `document end markers ("...") are not supported`, line: 2, col: 1},
		{name: "document end before content", in: "...\na: 1\n", msg: "document end markers", line: 1, col: 1},
		{name: "document end with a comment", in: "a: 1\n... # end\n", msg: "document end markers", line: 2, col: 1},
		{name: "document marker in a flow", in: "[a,\n--- , b]", msg: "multiple documents", line: 2, col: 1},
		{name: "document end in a flow", in: "[a,\n...\n]", msg: "document end markers", line: 2, col: 1},
		{name: "value on the --- line", in: "--- a: 1\n", msg: `a value on the "---" line is not supported`, line: 1, col: 5},
		{name: "empty input", in: "", msg: "empty document", line: 1, col: 1},
		{name: "only comments", in: "# a\n\n  # b\n", msg: "empty document", line: 1, col: 1},
		{name: "only the document start", in: "---\n", msg: "empty document", line: 1, col: 1},
		{name: "only spaces", in: "  \n ", msg: "empty document", line: 1, col: 1},
		{name: "nesting 101 deep", in: nested101, msg: "nested too deeply (max 100)", line: 101, col: 101},
		{name: "sequences 101 deep", in: strings.Repeat("- ", 101) + "a", msg: "nested too deeply (max 100)", line: 1, col: 201},
		{name: "flows 101 deep", in: strings.Repeat("[", 101) + strings.Repeat("]", 101), msg: "nested too deeply", line: 1, col: 101},
		{name: "flows in a mapping 101 deep", in: "a: " + strings.Repeat("{b: ", 100) + "1" + strings.Repeat("}", 100), msg: "nested too deeply", line: 1, col: 400},
		{name: "less indented key", in: "a:\n  b: 1\n c: 2\n", msg: indent, line: 3, col: 2},
		{name: "more indented key", in: "a: 1\n  b: 2\n", msg: indent, line: 2, col: 3},
		{name: "more indented key after a mapping", in: "a:\n  b: 1\n   c: 2\n", msg: indent, line: 3, col: 4},
		{name: "less indented item", in: "a:\n   - b\n  - c\n", msg: indent, line: 3, col: 3},
		{name: "more indented item", in: "- a\n - b\n", msg: indent, line: 2, col: 2},
		{name: "deeper line after a quoted value", in: "a: 'b'\n  c\n", msg: indent, line: 2, col: 3},
		{name: "deeper line after a comment", in: "a: b\n# c\n  d\n", msg: indent, line: 3, col: 3},
		{name: "deeper line after a comment on the line", in: "a: b # c\n  d\n", msg: indent, line: 2, col: 3},
		{name: "deeper line after a plain scalar two lines above", in: "a: b\nc: 'd'\n  e\n", msg: indent, line: 3, col: 3},
		{name: "less indented than the root", in: "  a: 1\nb: 2\n", msg: indent, line: 2, col: 1},
		{name: "less indented than a root sequence", in: "  - a\nb\n", msg: indent, line: 2, col: 1},
		{name: "item where a key is expected", in: "a: b\n- c\n", msg: `expected "key: value"`, line: 2, col: 1},
		{name: "flow where a key is expected", in: "a: b\n[c]: d\n", msg: `expected "key: value"`, line: 2, col: 1},
		{name: "key where an item is expected", in: "- a\nb: c\n", msg: `expected "- " for a sequence item`, line: 2, col: 1},
		{name: "scalar where an item is expected", in: "a:\n  - b\n  c\n", msg: `expected "- " for a sequence item`, line: 3, col: 3},
		{name: "text after the root value", in: "'a'\nb\n", msg: "unexpected text after the value", line: 2, col: 1},
		{name: "text after a root flow", in: "[a]\nb\n", msg: "unexpected text after the value", line: 2, col: 1},
		{name: "mapping in a value", in: "a: b: c\n", msg: `unexpected ":"; quote the value if it is a text`, line: 1, col: 5},
		{name: "colon in a value", in: "label: Note: x\n", msg: `unexpected ":"`, line: 1, col: 12},
		{name: "colon at the end of a value", in: "a: b:\n", msg: `unexpected ":"`, line: 1, col: 5},
		{name: "mapping in a compact mapping value", in: "- a: b: c\n", msg: `unexpected ":"`, line: 1, col: 7},
		{name: "mapping after a quoted value", in: "a: 'b': c\n", msg: "unexpected text after the closing quote", line: 1, col: 7},
		{name: "sequence in a value", in: "a: - b\n", msg: "a sequence cannot start on the same line as its key", line: 1, col: 4},
		{name: "dash as a value", in: "a: -\n", msg: "a sequence cannot start on the same line", line: 1, col: 4},
		{name: "missing colon", in: "a: 1\nb\n", msg: `expected "key: value"`, line: 2, col: 1},
		{name: "missing space after a colon", in: "a: 1\nb:2\n", msg: `expected "key: value"; put a space after ":"`, line: 2, col: 1},
		{name: "missing space after the colon of a quoted key", in: "a: 1\n'b':2\n", msg: `put a space after ":"`, line: 2, col: 1},
		{name: "missing space after the first quoted key", in: `"a":"b"`, msg: `missing space after ":"`, line: 1, col: 4},
		{name: "flow as a key", in: "[a]: b\n", msg: "[ ] and { } cannot be keys", line: 1, col: 1},
		{name: "flow as a key in a sequence", in: "- {a: b} : c\n", msg: "[ ] and { } cannot be keys", line: 1, col: 3},
		{name: "missing key", in: ": a\n", msg: `missing key before ":"`, line: 1, col: 1},
		{name: "missing key after a key", in: "a: 1\n: 2\n", msg: `missing key before ":"`, line: 2, col: 1},
		{name: "null", in: "a: null\n", msg: "null is not allowed; remove the key", line: 1, col: 4},
		{name: "Null", in: "a: Null # c\n", msg: "null is not allowed; remove the key", line: 1, col: 4},
		{name: "NULL", in: "a:\n  NULL\n", msg: "null is not allowed; remove the key", line: 2, col: 3},
		{name: "tilde", in: "a: ~\n", msg: "null is not allowed; remove the key", line: 1, col: 4},
		{name: "null item", in: "- a\n- ~\n", msg: "null is not allowed; remove the item", line: 2, col: 3},
		{name: "null in a flow sequence", in: "[a, null]", msg: "null is not allowed; remove the item", line: 1, col: 5},
		{name: "null in a flow mapping", in: "{a: ~}", msg: "null is not allowed; remove the key", line: 1, col: 5},
		{name: "null at the root", in: "null", msg: "null is not allowed", line: 1, col: 1},
		{name: "missing value at the end", in: "a:\n", msg: missing, line: 1, col: 1},
		{name: "missing value before a key", in: "a: 1\nb:\nc: 2\n", msg: missing, line: 2, col: 1},
		{name: "missing value with a comment", in: "a: # c\n# d\nb: 1\n", msg: missing, line: 1, col: 1},
		{name: "missing value before a less indented key", in: "a:\n  b:\nc: 1\n", msg: missing, line: 2, col: 3},
		{name: "missing item", in: "- a\n-\n- b\n", msg: missing, line: 2, col: 1},
		{name: "missing item at the end", in: "-", msg: missing, line: 1, col: 1},
		{name: "missing item before a key", in: "a:\n- # c\nb: 1\n", msg: missing, line: 2, col: 1},
		{name: "missing item in a compact sequence", in: "- -\n", msg: missing, line: 1, col: 3},
		{name: "missing value in a compact mapping", in: "- a:\n- b\n", msg: missing, line: 1, col: 3},
		{name: "value that starts with #", in: "primary: #1e73be\n", msg: missing + quoteHash, line: 1, col: 1},
		{name: "value that starts with # before a key", in: "a: #fff\nb: 1\n", msg: missing + quoteHash, line: 1, col: 1},
		{name: "comment after a key at the end", in: "a: # comment\n", msg: missing + quoteHash, line: 1, col: 1},
		{name: "item that starts with #", in: "colors:\n  - #fff\n", msg: missing + quoteHash, line: 2, col: 3},
		{name: "item that starts with # before an item", in: "- #fff\n- '#000'\n", msg: missing + quoteHash, line: 1, col: 1},
		{name: "value that starts with # in a compact mapping", in: "- a: #fff\n", msg: missing + quoteHash, line: 1, col: 3},
		{name: "item that starts with # in a compact sequence", in: "- - #fff\n", msg: missing + quoteHash, line: 1, col: 3},
		{name: "value that starts with # in a flow mapping", in: "a: {\n  b: #fff\n  }\n", msg: missing + quoteHash, line: 2, col: 3},
		{name: "missing value in a flow mapping", in: "{a: }", msg: missing, line: 1, col: 2},
		{name: "key without a value in a flow mapping", in: "{a}", msg: missing, line: 1, col: 2},
		{name: "key with a colon in a flow mapping", in: "{a:b}", msg: missing, line: 1, col: 2},
		{name: "missing value before a comma", in: "{a:,}", msg: missing, line: 1, col: 2},
		{name: "second key without a value", in: "{a: 1, b}", msg: missing, line: 1, col: 8},
		{name: "colon after a key on the next line", in: "{a\n:b}", msg: msgMultiLine, line: 2, col: 1},
		{name: "colon and a brace after a key on the next line", in: "{a\n:}", msg: missing, line: 1, col: 2},
		{name: "colon and a tab after a key on the next line", in: "{a\n:\tb}", msg: msgTab, line: 2, col: 2},
		{name: "number key", in: "1990: x\n", msg: `key "1990" must be a string; quote it`, line: 1, col: 1},
		{name: "boolean key", in: "a: 1\ntrue: x\n", msg: `key "true" must be a string; quote it`, line: 2, col: 1},
		{name: "null key", in: "null: x\n", msg: `key "null" must be a string`, line: 1, col: 1},
		{name: "tilde key", in: "- ~: x\n", msg: `key "~" must be a string`, line: 1, col: 3},
		{name: "octal key", in: "0o17: x\n", msg: `key "0o17" must be a string`, line: 1, col: 1},
		{name: "number key in a flow mapping", in: "{a: 1, 2: b}", msg: `key "2" must be a string`, line: 1, col: 8},
		{name: "1025 characters key", in: strings.Repeat("k", 1025) + ": v\n", msg: "key is longer than 1024 characters", line: 1, col: 1},
		{name: "1024 characters key and a space", in: "a: 1\n" + strings.Repeat("k", 1024) + " : v\n", msg: "key is longer than 1024 characters", line: 2, col: 1},
		{name: "1025 characters with the quotes", in: "- '" + strings.Repeat("k", 1023) + "': v\n", msg: "key is longer than 1024 characters", line: 1, col: 3},
		{name: "duplicate key", in: "a: 1\nb: 2\na: 3\n", msg: `duplicate key "a"`, line: 3, col: 1},
		{name: "duplicate quoted key", in: "a: 1\n\"a\": 2\n", msg: `duplicate key "a"`, line: 2, col: 1},
		{name: "duplicate escaped key", in: "a: 1\n\"\\x61\": 2\n", msg: `duplicate key "a"`, line: 2, col: 1},
		{name: "duplicate key in a flow mapping", in: "{a: 1, 'a': 2}", msg: `duplicate key "a"`, line: 1, col: 8},
		{name: "number with a plus sign", in: "a: +1\n", msg: `number "+1" must be written as in JSON, such as 10, -1.5 or 1e3; quote it for a text`, line: 1, col: 4},
		{name: "number with leading zeros", in: "a: 007\n", msg: `number "007" must be written as in JSON`, line: 1, col: 4},
		{name: "two zeros", in: "a: 00\n", msg: `number "00" must be written as in JSON`, line: 1, col: 4},
		{name: "hexadecimal", in: "a: 0x1F\n", msg: `number "0x1F" must be written as in JSON`, line: 1, col: 4},
		{name: "octal", in: "a: 0o17\n", msg: `number "0o17" must be written as in JSON`, line: 1, col: 4},
		{name: "one hexadecimal digit", in: "a: 0x1\n", msg: `number "0x1" must be written as in JSON`, line: 1, col: 4},
		{name: "one octal digit", in: "a: 0o7\n", msg: `number "0o7" must be written as in JSON`, line: 1, col: 4},
		{name: "fraction without a leading digit", in: "a: .5\n", msg: `number ".5" must be written as in JSON`, line: 1, col: 4},
		{name: "fraction with a sign and without a leading digit", in: "a: -.5e3\n", msg: `number "-.5e3" must be`, line: 1, col: 4},
		{name: "fraction without digits", in: "a: 1.\n", msg: `number "1." must be written as in JSON`, line: 1, col: 4},
		{name: "fraction without digits and an exponent", in: "a: 1.e3\n", msg: `number "1.e3" must be`, line: 1, col: 4},
		{name: "infinity", in: "a: .inf\n", msg: `number ".inf" must be written as in JSON`, line: 1, col: 4},
		{name: "infinity in capitals", in: "a: +.INF\n", msg: `number "+.INF" must be`, line: 1, col: 4},
		{name: "negative infinity", in: "a: -.Inf\n", msg: `number "-.Inf" must be`, line: 1, col: 4},
		{name: "not a number", in: "a: .NaN\n", msg: `number ".NaN" must be written as in JSON`, line: 1, col: 4},
		{name: "number in a flow", in: "[1, +2]", msg: `number "+2" must be written as in JSON`, line: 1, col: 5},
		{name: "literal block scalar", in: "a: |\n  x\n", msg: "block scalars (| and >) are not supported; write the value on one line", line: 1, col: 4},
		{name: "folded block scalar", in: "- >-\n  x\n", msg: "block scalars (| and >) are not supported", line: 1, col: 3},
		{name: "block scalar on the --- line", in: "--- |\n  x\n", msg: `a value on the "---" line`, line: 1, col: 5},
		{name: "block scalar at the root", in: "|\n  x\n", msg: "block scalars (| and >) are not supported", line: 1, col: 1},
		{name: "multi-line plain scalar", in: "a: b\n  c\n", msg: msgMultiLine, line: 2, col: 3},
		{name: "multi-line plain scalar after a blank line", in: "a: b\n\n  c\n", msg: msgMultiLine, line: 3, col: 3},
		{name: "multi-line plain scalar on its own lines", in: "a:\n  b\n  c\n", msg: msgMultiLine, line: 3, col: 3},
		{name: "multi-line plain item", in: "- a\n  b\n", msg: msgMultiLine, line: 2, col: 3},
		{name: "multi-line plain scalar at the root", in: "a\nb\n", msg: msgMultiLine, line: 2, col: 1},
		{name: "multi-line plain scalar in a flow", in: "[a\nb]", msg: msgMultiLine, line: 2, col: 1},
		{name: "multi-line plain key in a flow", in: "{a\n b: c}", msg: msgMultiLine, line: 2, col: 2},
		{name: "multi-line plain scalar before a dash", in: "[a\n- b]", msg: msgMultiLine, line: 2, col: 1},
		{name: "text after a quoted key", in: "\"a\"b: c\n", msg: "unexpected text after the closing quote", line: 1, col: 4},
		{name: "colon after spaces in a value", in: "a: b : c\n", msg: `unexpected ":"`, line: 1, col: 6},
		{name: "missing value of a key less indented than the key above", in: "a:\n  b:\n - x\n", msg: missing, line: 2, col: 3},
		{name: "item after a compact mapping", in: "- a: b\n  - c\n", msg: `expected "key: value"`, line: 2, col: 3},
		{name: "deeper entry after an item at the indentation of its key", in: "a:\n- b\n  c: d\n", msg: indent, line: 3, col: 3},
		{name: "multi-line plain scalar before a quote", in: "[a\n\"b\"]", msg: msgMultiLine, line: 2, col: 1},
		{name: "multi-line double-quoted", in: "a: \"b\n  c\"\n", msg: "missing closing quote; write the quoted value on one line", line: 1, col: 4},
		{name: "multi-line single-quoted", in: "a:\n  - 'b\n    c'\n", msg: "missing closing quote", line: 2, col: 5},
		{name: "unclosed quoted key", in: "'a: b\n", msg: "missing closing quote", line: 1, col: 1},
		{name: "escaped line break", in: "a: \"b\\\n  c\"\n", msg: `escaped line breaks (\ at the end of a line) are not supported; write the value on one line`, line: 1, col: 6},
		{name: "unknown escape", in: `a: "\q"`, msg: `unknown escape \q; write \\ for a backslash`, line: 1, col: 5},
		{name: "escaped single quote", in: `a: "it\'s"`, msg: `unknown escape \'`, line: 1, col: 7},
		{name: "unknown escape in Japanese", in: `名前: "\あ"`, msg: `unknown escape \あ`, line: 1, col: 6},
		{name: "short \\x", in: `a: "\x4"`, msg: `escape \x needs 2 hex digits`, line: 1, col: 5},
		{name: "short \\u", in: "a: \"\\u12g4\"", msg: `escape \u needs 4 hex digits`, line: 1, col: 5},
		{name: "short \\U at the end", in: `a: "\U0001F60`, msg: `escape \U needs 8 hex digits`, line: 1, col: 5},
		{name: "escape at the end of an unclosed quote", in: `a: "\x41`, msg: "missing closing quote", line: 1, col: 4},
		{name: "first hex digit not hex", in: `a: "\xg1"`, msg: `escape \x needs 2 hex digits`, line: 1, col: 5},
		{name: "surrogate pair", in: "a: \"\\ud83d\\ude00\"", msg: `surrogate escapes are not supported; write \U0001F600 or the character itself`, line: 1, col: 5},
		{name: "lone surrogate", in: "a: \"x\\udc00\"", msg: `surrogate escapes are not supported; write \U and 8 hex digits, or the character itself`, line: 1, col: 6},
		{name: "high surrogate without a low one", in: "a: \"\\ud83d\\u0041\"", msg: `write \U and 8 hex digits`, line: 1, col: 5},
		{name: "high surrogate before an \\x escape", in: "a: \"\\ud83d\\xdc00\"", msg: `write \U and 8 hex digits`, line: 1, col: 5},
		{name: "surrogate as \\U", in: `a: "\U0000D800"`, msg: `write \U and 8 hex digits`, line: 1, col: 5},
		{name: "last surrogate", in: "a: \"\\uDFFF\"", msg: `write \U and 8 hex digits`, line: 1, col: 5},
		{name: "first surrogate pair", in: "a: \"\\ud800\\udc00\"", msg: `write \U00010000 or the character itself`, line: 1, col: 5},
		{name: "last surrogate pair", in: "a: \"\\udbff\\udfff\"", msg: `write \U0010FFFF or the character itself`, line: 1, col: 5},
		{name: "high surrogate before a backslash at the end", in: "a: \"\\ud83d\\", msg: `write \U and 8 hex digits`, line: 1, col: 5},
		{name: "surrogate pair at the end", in: "a: \"\\ud83d\\ude00", msg: `write \U0001F600`, line: 1, col: 5},
		{name: "high surrogate before a short escape at the end", in: "a: \"\\ud83d\\ude0", msg: `write \U and 8 hex digits`, line: 1, col: 5},
		{name: "beyond U+10FFFF", in: `a: "\U00110000"`, msg: `escape \U00110000 is beyond U+10FFFF`, line: 1, col: 5},
		{name: "escaped DEL", in: `a: "\x7f"`, msg: "character U+007F is not allowed, even escaped", line: 1, col: 5},
		{name: "escaped C1 control", in: `a: "\x9F"`, msg: "character U+009F is not allowed, even escaped", line: 1, col: 5},
		{name: "escaped byte order mark", in: "a: \"\\ufeff\"", msg: "character U+FEFF is not allowed, even escaped", line: 1, col: 5},
		{name: "escaped noncharacter", in: `a: "\U0000FFFE"`, msg: "character U+FFFE is not allowed, even escaped", line: 1, col: 5},
		{name: "anchor", in: "a: &x 1\n", msg: "anchors (&) and aliases (*) are not supported; write each value in full", line: 1, col: 4},
		{name: "anchor on a key", in: "&x a: 1\n", msg: "anchors (&) and aliases (*) are not supported", line: 1, col: 1},
		{name: "alias", in: "a: 1\nb: *x\n", msg: "anchors (&) and aliases (*) are not supported", line: 2, col: 4},
		{name: "alias in a flow", in: "[*x]", msg: "anchors (&) and aliases (*) are not supported", line: 1, col: 2},
		{name: "tag", in: "a: !!str 1\n", msg: "tags (!) are not supported; remove the tag", line: 1, col: 4},
		{name: "local tag on an item", in: "- !x a\n", msg: "tags (!) are not supported", line: 1, col: 3},
		{name: "explicit key", in: "? a\n: b\n", msg: `explicit keys ("? ") are not supported; write "key: value"`, line: 1, col: 1},
		{name: "explicit key in a flow", in: "{? a: b}", msg: "explicit keys", line: 1, col: 2},
		{name: "explicit key in a flow sequence", in: "[? a]", msg: "explicit keys", line: 1, col: 2},
		{name: "at sign", in: "a: @x\n", msg: `unquoted text cannot start with "@"; quote it`, line: 1, col: 4},
		{name: "backquote", in: "a: `x`\n", msg: "unquoted text cannot start with \"`\"; quote it", line: 1, col: 4},
		{name: "percent sign", in: "a: %x\n", msg: `unquoted text cannot start with "%"`, line: 1, col: 4},
		{name: "comma", in: "a: ,x\n", msg: `unquoted text cannot start with ","`, line: 1, col: 4},
		{name: "closing bracket", in: "a: ]\n", msg: `unquoted text cannot start with "]"`, line: 1, col: 4},
		{name: "dash in a flow", in: "[-]", msg: `unquoted text cannot start with "-"`, line: 1, col: 2},
		{name: "dash and a space in a flow", in: "[- a]", msg: `unquoted text cannot start with "-"`, line: 1, col: 2},
		{name: "text after a quoted value", in: "a: \"b\" c\n", msg: "unexpected text after the closing quote", line: 1, col: 8},
		{name: "comment without a space after a quote", in: "a: \"b\"#c\n", msg: `a comment needs a space before "#"`, line: 1, col: 7},
		{name: "comment without a space after a flow", in: "a: [b]#c\n", msg: `a comment needs a space before "#"`, line: 1, col: 7},
		{name: "comment without a space after a quote in a flow", in: `["a"#b]`, msg: `a comment needs a space before "#"`, line: 1, col: 5},
		{name: "comment without a space after a comma", in: "[a,#b\n]", msg: `a comment needs a space before "#"`, line: 1, col: 4},
		{name: "mapping in a flow sequence", in: "[a: b]", msg: "a mapping in [ ] must be written in { }", line: 1, col: 3},
		{name: "quoted mapping in a flow sequence", in: `["a":b]`, msg: "a mapping in [ ] must be written in { }", line: 1, col: 5},
		{name: "mapping in a flow sequence over lines", in: "[a\n: b]", msg: "a mapping in [ ] must be written in { }", line: 2, col: 1},
		{name: "empty item", in: "[a,,b]", msg: `empty item; remove the extra ","`, line: 1, col: 4},
		{name: "only a comma", in: "[,]", msg: "empty item", line: 1, col: 2},
		{name: "empty item in a flow mapping", in: "{a: 1,,b: 2}", msg: "empty item", line: 1, col: 7},
		{name: "unclosed [", in: "a: [b\n", msg: `unclosed "["; to write a text that starts with "[", quote the whole value`, line: 1, col: 4},
		{name: "unclosed {", in: "a:\n  - {b: c\n", msg: `unclosed "{"; to write a text that starts with "{", quote the whole value`, line: 2, col: 5},
		{name: "unclosed nested [", in: "[a, [b]", msg: `unclosed "["`, line: 1, col: 1},
		{name: "unclosed [ at the root", in: "[a", msg: `unclosed "["`, line: 1, col: 1},
		{name: "extra ]", in: "a: [b]]\n", msg: `unexpected text after "]"; to write a text that starts with "[", quote the whole value`, line: 1, col: 7},
		{name: "extra ] at the root", in: "[b]]", msg: `unexpected text after "]"`, line: 1, col: 4},
		{name: "text after a flow", in: "a: [b] c\n", msg: `unexpected text after "]"`, line: 1, col: 8},
		{name: "colon after a flow value", in: "a: [b]: c\n", msg: `unexpected text after "]"`, line: 1, col: 7},
		{name: "title format", in: "titleFormat: {title} | {siteName}\n", msg: `missing value; to write a text that starts with "{", quote the whole value`, line: 1, col: 15},
		{name: "continuation of a flow at the key", in: "a: [b,\nc]\n", msg: `unexpected indentation; indent the lines of [ ] and { } deeper than their key or "-"`, line: 2, col: 1},
		{name: "closing bracket at the key", in: "a: [\n  b,\n]\n", msg: "unexpected indentation; indent the lines of [ ]", line: 3, col: 1},
		{name: "continuation of a flow at the dash", in: "- {a: b,\n  c: d,\n  e: f}\n- [\ng]\n", msg: "unexpected indentation", line: 5, col: 1},
		{name: "missing comma", in: `["a" b]`, msg: `expected "," or "]"`, line: 1, col: 6},
		{name: "missing comma in a flow mapping", in: "{a: 'b' c: d}", msg: `expected "," or "}"`, line: 1, col: 9},
		{name: "mapping in a flow mapping value", in: "{a: b: c}", msg: `expected "," or "}"`, line: 1, col: 6},
		{name: "missing colon in a flow mapping", in: `{"a" "b"}`, msg: `expected ":"`, line: 1, col: 6},
		{name: "mismatched closing bracket", in: "[a}", msg: `expected "," or "]"`, line: 1, col: 3},
		{name: "closing bracket of a sequence in a mapping", in: "{]}", msg: `unexpected "]"`, line: 1, col: 2},
		{name: "closing brace in a sequence", in: "[}]", msg: `unexpected "}"`, line: 1, col: 2},
		{name: "flow as a key in a flow mapping", in: "{[a]: b}", msg: "[ ] and { } cannot be keys", line: 1, col: 2},
		{name: "missing space after a colon in a flow mapping", in: "{a:[b]}", msg: `missing space after ":"`, line: 1, col: 4},
		{name: "missing space after a colon on the next line", in: "{a # c\n:b}", msg: `missing space after ":"`, line: 2, col: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, pos, err := ToJSON([]byte(tt.in))
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("ToJSON(%q) = %s, %v; want *Error", tt.in, out, err)
			}
			if out != nil || pos != nil {
				t.Errorf("ToJSON(%q) = %s, %v with an error; want nil", tt.in, out, pos)
			}
			if !strings.Contains(e.Msg, tt.msg) || e.Line != tt.line || e.Column != tt.col {
				t.Errorf("ToJSON(%q) = %v; want %d:%d: %s", tt.in, err, tt.line, tt.col, tt.msg)
			}
		})
	}
}

// TestToJSONHints checks whole messages, which TestToJSONErrors checks in part: the hint about quoting is only for
// block values, as a document is not a text, and the hint about the space after ":" only for keys with ":". The hint
// about "#" is only for a comment right after the ":" or "-", in place of the hint about quoting.
func TestToJSONHints(t *testing.T) {
	for in, want := range map[string]string{
		"[a] b":       `1:5: unexpected text after "]"`,
		"{a}":         "1:2: missing value",
		"a: [a] b":    `1:8: unexpected text after "]"; to write a text that starts with "[", quote the whole value`,
		"- {a}":       `1:4: missing value; to write a text that starts with "{", quote the whole value`,
		"a:\n  [[b":   `2:4: unclosed "["; to write a text that starts with "[", quote the whole value`,
		"a: 1\nb\n":   `2:1: expected "key: value"`,
		"a: 1\n'b'\n": `2:1: expected "key: value"`,
		"a: 1\nb:2\n": `2:1: expected "key: value"; put a space after ":"`,

		"primary: #1e73be\n":     `1:1: missing value; a "#" after a space starts a comment, so quote a value that starts with "#", such as "#1e73be"`,
		"colors:\n  - #fff\n":    `2:3: missing value; a "#" after a space starts a comment, so quote a value that starts with "#", such as "#1e73be"`,
		"a: {\n  b: #fff\n  }\n": `2:3: missing value; a "#" after a space starts a comment, so quote a value that starts with "#", such as "#1e73be"`,
		"a:\n  #fff\n":           "1:1: missing value",
		"{a:\n  # c\n}":          "1:2: missing value",
	} {
		if _, _, err := ToJSON([]byte(in)); err == nil || err.Error() != want {
			t.Errorf("ToJSON(%q) = %v, want %s", in, err, want)
		}
	}
}

func TestErrorString(t *testing.T) {
	err := &Error{Position: Position{Line: 3, Column: 5}, Msg: "missing value"}
	if got, want := err.Error(), "3:5: missing value"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
