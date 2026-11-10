package yaml

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func quoteJSON(s string) string {
	b, _ := jsontext.AppendQuote(nil, s)
	return string(b)
}

// jsonEscape returns the \u escape of r in a JSON string.
func jsonEscape(r rune) string {
	return fmt.Sprintf("%cu%04x", '\\', r)
}

// compactJSON returns data, a JSON value, without whitespace and with the strings escaped as ToJSON escapes them.
func compactJSON(t testing.TB, data []byte) string {
	t.Helper()
	v := jsontext.Value(bytes.Clone(data))
	if err := v.Compact(jsontext.PreserveRawStrings(false)); err != nil {
		t.Fatalf("Compact(%q) = %v", data, err)
	}
	return string(v)
}

// checkYAML checks out, which FromJSON wrote for in: it ends with a line break and has no space at the end of a line,
// ToJSON reads it as in, and FromJSON writes the same YAML for the JSON that ToJSON writes.
func checkYAML(t testing.TB, in string, out []byte) {
	t.Helper()
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Errorf("FromJSON(%q) = %q, which does not end with a line break", in, out)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("FromJSON(%q) = %q, which has a space at the end of a line", in, out)
			break
		}
	}
	got, _, err := ToJSON(out)
	if want := compactJSON(t, []byte(in)); err != nil || string(got) != want {
		t.Errorf("ToJSON(%q) = %s, %v; want %s", out, got, err, want)
		return
	}
	if again, err := FromJSON(got); err != nil || !bytes.Equal(again, out) {
		t.Errorf("FromJSON(%s) = %q, %v; want the YAML %q again", got, again, err, out)
	}
}

// TestFromJSONStrings checks when strings need quotes: the YAML of a string as a value, as an element of a list in
// the flow style, and as a key. An element or a key that the test omits is the same as the value.
func TestFromJSONStrings(t *testing.T) {
	acute := string(rune(0x0301))
	tests := []struct {
		s                   string
		value, element, key string
	}{
		{s: "true", value: `"true"`},
		{s: "True", value: `"True"`},
		{s: "false", value: `"false"`},
		{s: "null", value: `"null"`},
		{s: "~", value: `"~"`},
		{s: "10", value: `"10"`},
		{s: "-1.5", value: `"-1.5"`},
		{s: "1e3", value: `"1e3"`},
		{s: "007", value: `"007"`},
		{s: "0x1F", value: `"0x1F"`},
		{s: ".5", value: `".5"`},
		{s: "+1", value: `"+1"`},
		{s: "yes", value: `yes`},
		{s: "2026-10-30", value: `2026-10-30`},
		{s: "[PR] お知らせ", value: `"[PR] お知らせ"`},
		{s: "{x}", value: `"{x}"`},
		{s: "#1e73be", value: `"#1e73be"`},
		{s: "制作実績 #2", value: `"制作実績 #2"`},
		{s: "a#b", value: `a#b`},
		{s: "a: b", value: `"a: b"`},
		{s: "a:b", value: `a:b`},
		{s: "a:", value: `"a:"`, key: `a:`},
		{s: "- x", value: `"- x"`},
		{s: "-x", value: `-x`},
		{s: "-", value: `"-"`, key: `-`},
		{s: "? x", value: `"? x"`},
		{s: ":x", value: `:x`},
		{s: "@x", value: `"@x"`},
		{s: "`x", value: "\"`x\""},
		{s: "%x", value: `"%x"`},
		{s: "!x", value: `"!x"`},
		{s: "&x", value: `"&x"`},
		{s: "*x", value: `"*x"`},
		{s: "|", value: `"|"`},
		{s: ">", value: `">"`},
		{s: ",a", value: `",a"`},
		{s: "x,y", value: `x,y`, element: `"x,y"`},
		{s: "a]b", value: `a]b`, element: `"a]b"`},
		{s: " x", value: `" x"`},
		{s: "x ", value: `"x "`},
		{s: "", value: `""`},
		{s: "a\tb", value: `"a\tb"`},
		{s: "a\nb", value: `"a\nb"`},
		{s: "a\rb", value: `"a\rb"`},
		{s: "a" + string(rune(0)) + "b", value: `"a\x00b"`},
		{s: "a" + string(rune(0x1b)) + "b", value: `"a\x1Bb"`},
		{s: "a" + string(rune(0x85)) + "b", value: `"a\Nb"`},
		{s: "a" + string(rune(0x2028)) + "b", value: `"a\Lb"`},
		{s: "a" + string(rune(0x2029)) + "b", value: `"a\Pb"`},
		{s: "/works/{slug}/", value: `/works/{slug}/`, element: `"/works/{slug}/"`},
		{s: "日本語", value: `日本語`},
		{s: "Cafe" + acute, value: "Cafe" + acute},
		{s: "a" + string(rune(0xA0)) + "b", value: "a" + string(rune(0xA0)) + "b"},
		{s: "'quoted'", value: `"'quoted'"`},
		{s: `"quoted"`, value: `"\"quoted\""`},
		{s: `a'b"c`, value: `a'b"c`},
		{s: `a\b`, value: `a\b`},
		{s: `a\b #c`, value: `"a\\b #c"`},
		{s: "---", value: `---`},
		{s: "--- x", value: `--- x`, key: `"--- x"`},
		{s: "...", value: `...`},
	}
	for _, tt := range tests {
		element, key := cmp.Or(tt.element, tt.value), cmp.Or(tt.key, tt.value)
		q := quoteJSON(tt.s)
		for _, c := range []struct{ place, in, want string }{
			{"value", `{"k":` + q + `}`, "k: " + tt.value + "\n"},
			{"element", `{"k":[` + q + `]}`, "k: [" + element + "]\n"},
			{"key", `{` + q + `:0}`, key + ": 0\n"},
		} {
			t.Run(fmt.Sprintf("%s/%+q", c.place, tt.s), func(t *testing.T) {
				out, err := FromJSON([]byte(c.in))
				if err != nil || string(out) != c.want {
					t.Fatalf("FromJSON(%s) = %q, %v; want %q", c.in, out, err, c.want)
				}
				checkYAML(t, c.in, out)
			})
		}
	}
}

func TestFromJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "scalars", in: `{"a":1,"b":"x","c":true,"d":false}`, want: "a: 1\nb: x\nc: true\nd: false\n"},
		{name: "empty object", in: `{}`, want: "{}\n"},
		{name: "empty object with whitespace", in: " {\n}\n", want: "{}\n"},
		{name: "keys in the written order", in: `{"b":1,"a":{"d":1,"c":2}}`, want: "b: 1\na:\n  d: 1\n  c: 2\n"},
		{
			name: "nested objects",
			in:   `{"a":{"b":1,"c":{"d":"x"}},"e":"y"}`,
			want: "a:\n  b: 1\n  c:\n    d: x\ne: y\n",
		},
		{name: "lists of scalars", in: `{"a":["x","y"],"b":[1,true,"z"]}`, want: "a: [x, y]\nb: [1, true, z]\n"},
		{name: "empty collections", in: `{"a":{},"b":[]}`, want: "a: {}\nb: []\n"},
		{name: "empty collections in lists", in: `{"a":[{}],"b":[[]],"c":[{},[],1]}`, want: "a:\n  - {}\nb:\n  - []\nc:\n  - {}\n  - []\n  - 1\n"},
		{name: "list of objects", in: `{"a":[{"b":1,"c":2},{"d":3}]}`, want: "a:\n  - b: 1\n    c: 2\n  - d: 3\n"},
		{name: "list of lists", in: `{"a":[["x","y"],["z"],[]]}`, want: "a:\n  - [x, y]\n  - [z]\n  - []\n"},
		{name: "lists in lists", in: `{"a":[[{"b":1}],[[2]]]}`, want: "a:\n  - - b: 1\n  - - [2]\n"},
		{name: "three levels of lists", in: `{"a":[[[1]]]}`, want: "a:\n  - - [1]\n"},
		{
			name: "list with scalars and collections",
			in:   `{"a":[1,{"b":2},[3],"x",[4,{"c":5}]]}`,
			want: "a:\n  - 1\n  - b: 2\n  - [3]\n  - x\n  - - 4\n    - c: 5\n",
		},
		{
			name: "strings in a list with objects",
			in:   `{"a":["-x",":y","a: b","",{"c":"true"}]}`,
			want: "a:\n  - -x\n  - :y\n  - \"a: b\"\n  - \"\"\n  - c: \"true\"\n",
		},
		{
			name: "collections in objects in lists",
			in:   `{"a":[{"b":{"c":1},"d":[{"e":2}],"f":[3],"g":{}}]}`,
			want: "a:\n  - b:\n      c: 1\n    d:\n      - e: 2\n    f: [3]\n    g: {}\n",
		},
		{
			name: "numbers as written",
			in:   `{"a":1.50,"b":-0,"c":1E5,"d":[1e+2,0.5],"e":12345678901234567890}`,
			want: "a: 1.50\nb: -0\nc: 1E5\nd: [1e+2, 0.5]\ne: 12345678901234567890\n",
		},
		{
			name: "keys",
			in:   `{"true":1,"a b":2,"":3,"1":4,"a: b":5,"x":6}`,
			want: "\"true\": 1\na b: 2\n\"\": 3\n\"1\": 4\n\"a: b\": 5\nx: 6\n",
		},
		{name: "keys of mappings in lists", in: `{"a":[{"1":{"true":[{"":0}]}}]}`, want: "a:\n  - \"1\":\n      \"true\":\n        - \"\": 0\n"},
		{
			name: "whitespace and escapes of the JSON",
			in:   " {\n \"a\" : \"" + jsonEscape('A') + `\/` + "\" ,\n \"b\" : [ 1 , 2 ] } ",
			want: "a: A/\nb: [1, 2]\n",
		},
		{name: "Japanese", in: `{"名前":"値","キー":["あ","い"]}`, want: "名前: 値\nキー: [あ, い]\n"},
		{
			name: "strings in lists",
			in:   `{"a":["x y","a,b","true","","/a/{b}/","[c]"]}`,
			want: "a: [x y, \"a,b\", \"true\", \"\", \"/a/{b}/\", \"[c]\"]\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []byte(tt.in)
			out, err := FromJSON(in)
			if err != nil || string(out) != tt.want {
				t.Fatalf("FromJSON(%q) = %q, %v; want %q", tt.in, out, err, tt.want)
			}
			if string(in) != tt.in {
				t.Errorf("FromJSON changed its input to %q", in)
			}
			checkYAML(t, tt.in, out)
		})
	}
}

// TestFromJSONWorksSchema checks the example of site/schema.yaml in 6.4.4 of the concept, without its comments.
func TestFromJSONWorksSchema(t *testing.T) {
	const want = `schemaVersion: 1
types:
  works:
    label: 制作実績
    url: /works/{slug}/
    archive: /works/
    taxonomies: [works_category]
    body: true
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
        translatable: false
      images:
        type: list
        label: 画像
        of:
          type: media
          accept: [image]
        maxItems: 20
taxonomies:
  works_category:
    label: 実績の種類
    hierarchical: false
`
	data, _, err := ToJSON([]byte(worksSchema))
	if err != nil {
		t.Fatal(err)
	}
	out, err := FromJSON(data)
	if err != nil || string(out) != want {
		t.Fatalf("FromJSON(%s) = %q, %v; want %q", data, out, err, want)
	}
	checkYAML(t, string(data), out)
}

// TestFromJSONReads checks that FromJSON writes YAML that ToJSON reads back for the JSON that ToJSON writes for the
// tests of ToJSON and for yaml-test-suite.
func TestFromJSONReads(t *testing.T) {
	var inputs []string
	for _, tt := range toJSONTests {
		// One of its keys is in a flow mapping and too long to write in the block style.
		if tt.name != "long keys" {
			inputs = append(inputs, tt.in)
		}
	}
	for _, tt := range readSuite(t) {
		inputs = append(inputs, tt.YAML)
	}
	n := 0
	for _, in := range inputs {
		data, _, err := ToJSON([]byte(in))
		if err != nil || !jsontext.Value(data).IsValid() || data[0] != '{' {
			continue
		}
		n++
		out, err := FromJSON(data)
		if err != nil {
			t.Errorf("FromJSON(%s) = %v", data, err)
			continue
		}
		checkYAML(t, string(data), out)
	}
	t.Logf("wrote %d objects", n)
	if n < 50 {
		t.Errorf("FromJSON wrote %d objects, want more", n)
	}
}

// randomJSON writes random JSON objects, with strings of the pieces that are hard for YAML.
type randomJSON struct {
	rng    *rand.Rand
	pieces []string
}

func (g *randomJSON) text() string {
	var b strings.Builder
	for range g.rng.IntN(6) {
		b.WriteString(g.pieces[g.rng.IntN(len(g.pieces))])
	}
	return b.String()
}

// value returns a value in depth levels of collections.
func (g *randomJSON) value(depth int) string {
	scalars := []string{"0", "-1", "1.50", "1e3", "-0.5E+2", "12345678901234567890", "true", "false"}
	k := g.rng.IntN(10)
	switch {
	case k < 3 || depth >= 6:
		return quoteJSON(g.text())
	case k < 4:
		return scalars[g.rng.IntN(len(scalars))]
	case k < 7:
		var members []string
		seen := make(map[string]bool)
		for range g.rng.IntN(5) {
			if name := g.text(); !seen[name] {
				seen[name] = true
				members = append(members, quoteJSON(name)+":"+g.value(depth+1))
			}
		}
		return "{" + strings.Join(members, ",") + "}"
	}
	var elems []string
	for range g.rng.IntN(5) {
		if g.rng.IntN(3) == 0 {
			elems = append(elems, scalars[g.rng.IntN(len(scalars))])
		} else {
			elems = append(elems, g.value(depth+1))
		}
	}
	return "[" + strings.Join(elems, ",") + "]"
}

// TestFromJSONRandom checks that FromJSON writes YAML that ToJSON reads back, at every place of strings in objects
// and lists, with the strings of the pieces that are hard for YAML.
func TestFromJSONRandom(t *testing.T) {
	g := &randomJSON{rng: rand.New(rand.NewPCG(1, 2))}
	g.pieces = []string{
		"a", "b", "Z", "0", "1", "7", ".", "e", "E", "+", "-", " ", "  ", ":", "#", "?", "[", "]", "{", "}", ",", `"`, "'",
		`\`, "\t", "\n", "\r", "~", "&", "*", "!", "|", ">", "%", "@", "`", "/", "_", "日", "true", "false", "null", "yes",
		"---", "...", "- ", ": ", " #", "k: v", string(rune(1)), string(rune(0x85)), string(rune(0x2028)),
		string(rune(0xA0)), string(rune(0x3000)), string(rune(0x1F600)), string(rune(0xFFFD)),
	}
	for range 10000 {
		in := "{" + quoteJSON(g.text()) + ":" + g.value(1) + "}"
		out, err := FromJSON([]byte(in))
		if err != nil {
			t.Fatalf("FromJSON(%s) = %v", in, err)
		}
		checkYAML(t, in, out)
		if t.Failed() {
			t.FailNow()
		}
	}
}

func TestFromJSONLimits(t *testing.T) {
	_, nested100JSON := nested(100)
	_, nested101JSON := nested(101)
	arrays := func(n int) string { return `{"a":` + strings.Repeat("[", n) + "1" + strings.Repeat("]", n) + "}" }
	// n objects in an object in a list: n + 2 levels.
	objects := func(n int) string {
		return `{"a":[` + strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n) + "]}"
	}
	ok := []string{
		nested100JSON,
		arrays(99),
		`{"a":` + strings.Repeat("[", 99) + strings.Repeat("]", 99) + "}",
		`{"a":` + strings.Repeat("[", 98) + `{"b":1}` + strings.Repeat("]", 98) + "}",
		objects(98),
		// Keys of 1024 characters as written: the first is plain, and the second has the quotes.
		`{"` + strings.Repeat("k", 1024) + `":1,"` + strings.Repeat("0", 1022) + `":2}`,
		`{"a":{"` + strings.Repeat("k", 1024) + `":1}}`,
	}
	for i, in := range ok {
		out, err := FromJSON([]byte(in))
		if err != nil {
			t.Errorf("FromJSON(%d) = %v", i, err)
			continue
		}
		checkYAML(t, in, out)
	}

	bad := []struct{ in, msg string }{
		{nested101JSON, "FromJSON: nested too deeply (max 100)"},
		{arrays(100), "FromJSON: nested too deeply (max 100)"},
		{objects(99), "FromJSON: nested too deeply (max 100)"},
		{`{"` + strings.Repeat("k", 1025) + `":1}`, "is longer than 1024 characters"},
		{`{"a":{"` + strings.Repeat("0", 1023) + `":1}}`, "is longer than 1024 characters"},
		{`{"a":[{"` + strings.Repeat(string(rune(0x3042)), 1025) + `":1}]}`, "is longer than 1024 characters"},
	}
	for i, tt := range bad {
		out, err := FromJSON([]byte(tt.in))
		if err == nil || !strings.Contains(err.Error(), tt.msg) || out != nil {
			t.Errorf("FromJSON(%d) = %q, %v; want the error %q", i, out, err, tt.msg)
		}
	}
}

func TestFromJSONErrors(t *testing.T) {
	str := func(r rune) string { return `{"a":` + quoteJSON("x"+string(r)+"y") + "}" }
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "null", in: `{"a":null}`, msg: "FromJSON: null is not in the YAML that ToJSON reads"},
		{name: "null in a list", in: `{"a":[1,null]}`, msg: "FromJSON: null is not in the YAML that ToJSON reads"},
		{name: "null in a nested object", in: `{"a":{"b":[{"c":null}]}}`, msg: "FromJSON: null is not in the YAML that ToJSON reads"},
		{name: "null at the top", in: `null`, msg: "FromJSON: the JSON must be an object"},
		{name: "array at the top", in: `[{"a":1}]`, msg: "FromJSON: the JSON must be an object"},
		{name: "string at the top", in: `"a"`, msg: "FromJSON: the JSON must be an object"},
		{name: "number at the top", in: `1`, msg: "FromJSON: the JSON must be an object"},
		{name: "empty input", in: ``, msg: "FromJSON: the JSON must be an object"},
		{name: "blank input", in: " \n", msg: "FromJSON: the JSON must be an object"},
		{name: "DEL", in: str(0x7F), msg: `FromJSON: the string "x\x7fy" has the character U+007F, which YAML cannot have`},
		{name: "start of the C1 controls", in: str(0x80), msg: `has the character U+0080, which YAML cannot have`},
		{name: "end of the C1 controls", in: str(0x9F), msg: `has the character U+009F, which YAML cannot have`},
		{name: "U+FEFF", in: str(0xFEFF), msg: `has the character U+FEFF, which YAML cannot have`},
		{name: "U+FFFE", in: str(0xFFFE), msg: `has the character U+FFFE, which YAML cannot have`},
		{name: "U+FFFF", in: str(0xFFFF), msg: `has the character U+FFFF, which YAML cannot have`},
		{name: "character in a key", in: `{` + quoteJSON("x"+string(rune(0x7F))) + `:1}`, msg: `which YAML cannot have`},
		{name: "character in a list", in: `{"a":[1,` + quoteJSON(string(rune(0x90))) + `]}`, msg: `which YAML cannot have`},
		{name: "character in a nested object", in: `{"a":{"b":[{"c":` + quoteJSON(string(rune(0xFFFE))) + `}]}}`, msg: `which YAML cannot have`},
		{name: "invalid value", in: `{"a":}`, msg: "FromJSON: jsontext: "},
		{name: "missing colon", in: `{"a" 1}`, msg: "FromJSON: jsontext: "},
		{name: "unclosed object", in: `{"a":1`, msg: "FromJSON: "},
		{name: "unclosed list", in: `{"a":[1`, msg: "FromJSON: "},
		{name: "unclosed string", in: `{"a":"x`, msg: "FromJSON: "},
		{name: "text", in: `x`, msg: "FromJSON: jsontext: "},
		{name: "text after the object", in: `{"a":1} x`, msg: "FromJSON: jsontext: "},
		{name: "second object", in: `{"a":1} {"b":2}`, msg: "FromJSON: unexpected data after the object"},
		{name: "closing brace", in: `{"a":1}}`, msg: "FromJSON: jsontext: "},
		{name: "duplicate key", in: `{"a":1,"a":2}`, msg: "duplicate object member name"},
		{name: "duplicate key in a nested object", in: `{"a":{"b":1,"c":2,"b":3}}`, msg: "duplicate object member name"},
		{name: "duplicate key with an escape", in: `{"a":1,"` + jsonEscape('a') + `":2}`, msg: "duplicate object member name"},
		{name: "invalid UTF-8", in: "{\"a\":\"\xff\"}", msg: "invalid UTF-8"},
		{name: "invalid UTF-8 in a key", in: "{\"\xff\":1}", msg: "invalid UTF-8"},
		{name: "lone surrogate", in: `{"a":"` + jsonEscape(0xD800) + `"}`, msg: "FromJSON: jsontext: "},
		{name: "trailing comma", in: `{"a":1,}`, msg: "FromJSON: jsontext: "},
		{name: "comment", in: `{"a":1 /* x */}`, msg: "FromJSON: jsontext: "},
		{name: "single quotes", in: `{'a':1}`, msg: "FromJSON: jsontext: "},
		{name: "byte order mark", in: "\xef\xbb\xbf{}", msg: "FromJSON: jsontext: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := FromJSON([]byte(tt.in))
			if err == nil || !strings.Contains(err.Error(), tt.msg) || out != nil {
				t.Errorf("FromJSON(%q) = %q, %v; want the error %q", tt.in, out, err, tt.msg)
			}
		})
	}
}
