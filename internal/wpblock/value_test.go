package wpblock

import (
	"fmt"
	"strings"
	"testing"
)

// uEsc starts a \u escape of JSON.
const uEsc = "\\" + "u"

const invalid = "invalid"

var (
	eAcute  = string(rune(0xe9))
	euro    = string(rune(0x20ac))
	lineSep = string(rune(0x2028))
	grin    = string(rune(0x1f600))
)

// nestObjects returns n objects, each the value of the member a of the one around it, with inner in the innermost.
func nestObjects(n int, inner string) string {
	return strings.Repeat(`{"a":`, n) + inner + strings.Repeat("}", n)
}

var decodeTests = []struct {
	name string
	in   string
	want string // as jsonText writes the value, or invalid
}{
	{"empty object", `{}`, `{}`},
	{"object", `{"a":1,"b":"x","c":true,"d":false,"e":null}`, `{"a":1,"b":"x","c":true,"d":false,"e":null}`},
	{"arrays", `{"a":[],"b":[1,[2,[3]]],"c":{}}`, `{"a":[],"b":[1,[2,[3]]],"c":{}}`},
	{"white space", " \t\r\n{ \"a\" :\t[ 1 ,\n2 ] } \t\r\n", `{"a":[1,2]}`},
	{"empty name", `{"":1}`, `{"":1}`},
	{"top level array", `[1,2]`, `[1,2]`},
	{"top level string", `"x"`, `"x"`},
	{"top level number", `1`, `1`},
	{"top level null", `null`, `null`},
	{"nothing", ``, invalid},
	{"only white space", ` `, invalid},

	// Numbers.
	{"zero", `{"a":0}`, `{"a":0}`},
	{"negative", `{"a":-1}`, `{"a":-1}`},
	{"fraction", `{"a":1.50}`, `{"a":1.50}`},
	{"exponent", `{"a":1e2,"b":1E+2,"c":1e-2,"d":0.5e1}`, `{"a":1e2,"b":1E+2,"c":1e-2,"d":0.5e1}`},
	{"negative zero", `{"a":-0}`, `{"a":-0}`},
	{"too big for a float", `{"a":1e400}`, `{"a":1e400}`},
	{"too big for an integer", `{"a":123456789012345678901234567890}`, `{"a":123456789012345678901234567890}`},
	{"leading zero", `{"a":01}`, invalid},
	{"plus sign", `{"a":+1}`, invalid},
	{"no digit before the point", `{"a":.5}`, invalid},
	{"no digit after the point", `{"a":1.}`, invalid},
	{"point and exponent", `{"a":1.e3}`, invalid},
	{"no digit in the exponent", `{"a":1e}`, invalid},
	{"hexadecimal", `{"a":0x10}`, invalid},
	{"only a sign", `{"a":-}`, invalid},
	{"NaN", `{"a":NaN}`, invalid},
	{"Infinity", `{"a":Infinity}`, invalid},
	{"number and letter", `{"a":1a}`, invalid},

	// Literals.
	{"upper case true", `{"a":TRUE}`, invalid},
	{"capital true", `{"a":True}`, invalid},
	{"upper case null", `{"a":NULL}`, invalid},
	{"mixed case false", `{"a":fAlse}`, invalid},
	{"short null", `{"a":nul}`, invalid},
	{"undefined", `{"a":undefined}`, invalid},

	// Syntax.
	{"open object", `{`, invalid},
	{"no value", `{"a"}`, invalid},
	{"no value after the colon", `{"a":}`, invalid},
	{"trailing comma", `{"a":1,}`, invalid},
	{"trailing comma in an array", `{"a":[1,]}`, invalid},
	{"only a comma", `{,}`, invalid},
	{"no comma", `{"a":1 "b":2}`, invalid},
	{"no comma in an array", `{"a":[1 2]}`, invalid},
	{"no colon", `{"a" 1}`, invalid},
	{"bare name", `{a:1}`, invalid},
	{"single quotes", `{'a':1}`, invalid},
	{"comment", `{"a":1/*x*/}`, invalid},
	{"line comment", "{\"a\":1}//x", invalid},
	{"name that is not a string", `{1:1}`, invalid},
	{"mismatched brackets", `{"a":[1}]`, invalid},
	{"unclosed array", `{"a":[1}`, invalid},

	// Text after the value.
	{"two objects", `{}{}`, invalid},
	{"two objects with white space", `{} {}`, invalid},
	{"letter after", `{} x`, invalid},
	{"FF after", "{}\f", invalid},
	{"FF before", "\f{}", invalid},
	{"vertical tab after", "{}\v", invalid},
	{"NUL after", "{}\x00", invalid},
	{"BOM before", "\xef\xbb\xbf{}", invalid},
	{"white space after", "{} \t\r\n", `{}`},

	// Strings.
	{"escapes", `{"a":"\"\\\/\b\f\n\r\t"}`, `{"a":"\"\\/` + uEsc + `0008` + uEsc + `000c` + uEsc + `000a` + uEsc + `000d` + uEsc + `0009"}`},
	{"escaped control character", `{"a":"` + uEsc + `0001` + uEsc + `001F"}`, `{"a":"` + uEsc + `0001` + uEsc + `001f"}`},
	{"escaped NUL", `{"a":"` + uEsc + `0000"}`, `{"a":"` + uEsc + `0000"}`},
	{"escaped NUL in a name", `{"` + uEsc + `0000a":1}`, `{"` + uEsc + `0000a":1}`},
	{"escaped letter", `{"` + uEsc + `0061":"` + uEsc + `00E9"}`, `{"a":"` + eAcute + `"}`},
	{"surrogate pair", `{"a":"` + uEsc + `d83d` + uEsc + `de00"}`, `{"a":"` + grin + `"}`},
	{"surrogate pair in upper case", `{"a":"` + uEsc + `D83D` + uEsc + `DE00"}`, `{"a":"` + grin + `"}`},
	{"unpaired high surrogate", `{"a":"` + uEsc + `d800"}`, invalid},
	{"unpaired low surrogate", `{"a":"` + uEsc + `dc00"}`, invalid},
	{"high surrogate and a letter", `{"a":"` + uEsc + `d800a"}`, invalid},
	{"high surrogate and an escape", `{"a":"` + uEsc + `d800` + uEsc + `0041"}`, invalid},
	{"two high surrogates", `{"a":"` + uEsc + `d800` + uEsc + `d800"}`, invalid},
	{"low surrogate and high surrogate", `{"a":"` + uEsc + `dc00` + uEsc + `d800"}`, invalid},
	{"unpaired surrogate in a name", `{"` + uEsc + `d800":1}`, invalid},
	{"short escape", `{"a":"` + uEsc + `12"}`, invalid},
	{"escape that is not hexadecimal", `{"a":"` + uEsc + `12G4"}`, invalid},
	{"unknown escape", `{"a":"\x"}`, invalid},
	{"escape of a letter", `{"a":"\a"}`, invalid},
	{"single escape character", `{"a":"\"}`, invalid},
	{"control character", "{\"a\":\"x\x01y\"}", invalid},
	{"LF", "{\"a\":\"x\ny\"}", invalid},
	{"tab", "{\"a\":\"x\ty\"}", invalid},
	{"DEL", "{\"a\":\"x\x7fy\"}", "{\"a\":\"x\x7fy\"}"},
	{"U+2028", `{"a":"` + lineSep + `"}`, `{"a":"` + lineSep + `"}`},
	{"invalid UTF-8", "{\"a\":\"x\xffy\"}", invalid},
	{"truncated UTF-8", "{\"a\":\"x\xe3\x81\"}", invalid},
	{"overlong UTF-8", "{\"a\":\"x\xc0\x80y\"}", invalid},
	{"surrogate in UTF-8", "{\"a\":\"x\xed\xa0\x80y\"}", invalid},
	{"code point over the limit", "{\"a\":\"x\xf4\x90\x80\x80y\"}", invalid},
	{"invalid UTF-8 in a name", "{\"a\xff\":1}", invalid},
	{"invalid UTF-8 outside strings", "{\xff}", invalid},
	{"unclosed string", `{"a":"x}`, invalid},

	// Repeated names.
	{"repeated name", `{"a":1,"b":2,"a":3}`, `{"a":3,"b":2}`},
	{"repeated name in an object in an array", `{"a":[{"b":1,"b":2}]}`, `{"a":[{"b":2}]}`},
	{"repeated name as an escape", `{"a":1,"` + uEsc + `0061":2}`, `{"a":2}`},
	{"names that look like numbers", `{"1":1,"01":2,"1":3}`, `{"1":3,"01":2}`},
	{"repeated name with another kind of value", `{"a":{"b":1},"a":[2]}`, `{"a":[2]}`},
}

func TestDecodeAttrs(t *testing.T) {
	for _, tt := range decodeTests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := DecodeAttrs(tt.in)
			if tt.want == invalid {
				if ok || v != nil {
					t.Errorf("DecodeAttrs(%q) = %s, %v; want it to be invalid", tt.in, jsonText(v), ok)
				}
				return
			}
			if !ok || v == nil {
				t.Fatalf("DecodeAttrs(%q) is invalid, want %s", tt.in, tt.want)
			}
			if got := jsonText(v); got != tt.want {
				t.Errorf("DecodeAttrs(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecodeAttrsStrings(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`"\"\\\/\b\f\n\r\t"`, "\"\\/\b\f\n\r\t"},
		{`"` + uEsc + `00e9 ` + uEsc + `20AC"`, eAcute + " " + euro},
		{`"` + uEsc + `d83d` + uEsc + `de00"`, grin},
		{`"` + uEsc + `0000"`, "\x00"},
		{`"` + eAcute + grin + `"`, eAcute + grin},
	}
	for _, tt := range tests {
		v, ok := DecodeAttrs(tt.in)
		if !ok {
			t.Errorf("DecodeAttrs(%q) is invalid", tt.in)
			continue
		}
		if got, isString := v.Str(); !isString || got != tt.want {
			t.Errorf("DecodeAttrs(%q) = %q, %v; want %q", tt.in, got, isString, tt.want)
		}
	}
}

func TestDecodeAttrsDepth(t *testing.T) {
	deepest := MaxAttrsDepth
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"objects at the limit", nestObjects(deepest, "1"), true},
		{"objects above the limit", nestObjects(deepest+1, "1"), false},
		{"empty object at the limit", nestObjects(deepest-1, "{}"), true},
		{"empty object above the limit", nestObjects(deepest, "{}"), false},
		{"arrays at the limit", strings.Repeat("[", deepest) + strings.Repeat("]", deepest), true},
		{"arrays above the limit", strings.Repeat("[", deepest+1) + strings.Repeat("]", deepest+1), false},
		{"arrays with a number at the limit", strings.Repeat("[", deepest) + "1" + strings.Repeat("]", deepest), true},
		{"arrays with a number above the limit", strings.Repeat("[", deepest+1) + "1" + strings.Repeat("]", deepest+1), false},
		{"mixed at the limit", strings.Repeat(`{"a":[`, deepest/2) + "{}" + strings.Repeat(`]}`, deepest/2), true},
		{"mixed above the limit", strings.Repeat(`{"a":[`, deepest/2+1) + "1" + strings.Repeat(`]}`, deepest/2+1), false},
		{"open brackets above the limit", strings.Repeat("[", deepest+1), false},
		{"far above the limit", strings.Repeat("[", 200000) + strings.Repeat("]", 200000), false},
		{"far above the limit, and open", strings.Repeat(`{"a":`, 200000), false},
	}
	for _, tt := range tests {
		v, ok := DecodeAttrs(tt.in)
		if ok != tt.ok || (v != nil) != tt.ok {
			t.Errorf("%s: DecodeAttrs of %d bytes = %v, %v; want %v", tt.name, len(tt.in), v != nil, ok, tt.ok)
		}
	}
	// The value comes out with the levels that were written.
	v, ok := DecodeAttrs(strings.Repeat("[", deepest) + "1" + strings.Repeat("]", deepest))
	if !ok {
		t.Fatal("arrays at the limit are invalid")
	}
	levels := 0
	for v.Kind == Array {
		levels++
		if len(v.Array) != 1 {
			t.Fatalf("level %d has %d elements, want 1", levels, len(v.Array))
		}
		v = &v.Array[0]
	}
	if levels != deepest || v.Kind != Number || v.Number != "1" {
		t.Errorf("read %d levels, then %v %q; want %d levels, then the number 1", levels, v.Kind, v.Number, deepest)
	}
}

func TestDecodeAttrsManyMembers(t *testing.T) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i := range 100 {
		fmt.Fprintf(&sb, `"k%d":%d,`, i, i)
	}
	// Names that are repeated after the first linear search has turned to an index.
	sb.WriteString(`"k3":"three","k50":"fifty","k99":"last","new":1,"new":2}`)
	v, ok := DecodeAttrs(sb.String())
	if !ok {
		t.Fatal("DecodeAttrs is invalid")
	}
	if len(v.Object) != 101 {
		t.Fatalf("%d members, want 101", len(v.Object))
	}
	for i := range 100 {
		m := v.Object[i]
		want := fmt.Sprint(i)
		switch i {
		case 3:
			want = `"three"`
		case 50:
			want = `"fifty"`
		case 99:
			want = `"last"`
		}
		if m.Name != fmt.Sprintf("k%d", i) || jsonText(&m.Value) != want {
			t.Errorf("member %d is %q: %s, want k%d: %s", i, m.Name, jsonText(&m.Value), i, want)
		}
	}
	if m := v.Object[100]; m.Name != "new" || jsonText(&m.Value) != "2" {
		t.Errorf("last member is %q: %s, want new: 2", m.Name, jsonText(&m.Value))
	}
}

func TestValueAccessors(t *testing.T) {
	v, ok := DecodeAttrs(`{"s":"x","i":3,"f":1.5,"t":true,"n":null,"o":{"a":1},"l":[1],"neg":-4,"big":9223372036854775808}`)
	if !ok {
		t.Fatal("DecodeAttrs is invalid")
	}
	if v.Get("missing") != nil || v.Get("s").Get("a") != nil || v.Get("l").Get("0") != nil {
		t.Error("Get of a missing member, or of a value that is not an object, is not nil")
	}
	var nilValue *Value
	if nilValue.Get("a") != nil {
		t.Error("Get of nil is not nil")
	}
	if got := v.Get("o").Get("a"); got == nil || got.Number != "1" {
		t.Errorf(`Get("o").Get("a") = %v, want the number 1`, got)
	}
	if s, ok := v.Get("s").Str(); !ok || s != "x" {
		t.Errorf(`Str of "x" = %q, %v`, s, ok)
	}
	for _, name := range []string{"i", "t", "n", "o", "l", "missing"} {
		if s, ok := v.Get(name).Str(); ok || s != "" {
			t.Errorf("Str of %q = %q, %v; want false", name, s, ok)
		}
	}
	if b, ok := v.Get("t").IsTrue(); !ok || !b {
		t.Errorf("IsTrue of true = %v, %v", b, ok)
	}
	if b, ok := nilValue.IsTrue(); ok || b {
		t.Errorf("IsTrue of nil = %v, %v", b, ok)
	}
	if b, ok := v.Get("s").IsTrue(); ok || b {
		t.Errorf("IsTrue of a string = %v, %v", b, ok)
	}
	f, _ := DecodeAttrs(`[false]`)
	if b, ok := f.Array[0].IsTrue(); !ok || b {
		t.Errorf("IsTrue of false = %v, %v; want false, true", b, ok)
	}
	if n, ok := v.Get("i").Int(); !ok || n != 3 {
		t.Errorf("Int of 3 = %d, %v", n, ok)
	}
	if n, ok := v.Get("neg").Int(); !ok || n != -4 {
		t.Errorf("Int of -4 = %d, %v", n, ok)
	}
	for _, name := range []string{"f", "s", "t", "n", "big", "missing"} {
		if n, ok := v.Get(name).Int(); ok || n != 0 {
			t.Errorf("Int of %q = %d, %v; want false", name, n, ok)
		}
	}
	if x, ok := v.Get("f").Float(); !ok || x != 1.5 {
		t.Errorf("Float of 1.5 = %v, %v", x, ok)
	}
	if x, ok := v.Get("i").Float(); !ok || x != 3 {
		t.Errorf("Float of 3 = %v, %v", x, ok)
	}
	if x, ok := v.Get("big").Float(); !ok || x != 9223372036854775808 {
		t.Errorf("Float of 2^63 = %v, %v", x, ok)
	}
	for _, name := range []string{"s", "t", "missing"} {
		if x, ok := v.Get(name).Float(); ok || x != 0 {
			t.Errorf("Float of %q = %v, %v; want false", name, x, ok)
		}
	}
}

func TestInt(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"-0", 0, true},
		{"0.0", 0, true},
		{"0e5", 0, true},
		{"0e99999999999999999999", 0, true},
		{"3", 3, true},
		{"-3", -3, true},
		{"3.0", 3, true},
		{"3.000", 3, true},
		{"3e0", 3, true},
		{"3E0", 3, true},
		{"3.5", 0, false},
		{"3.05", 0, false},
		{"0.5", 0, false},
		{"1e2", 100, true},
		{"1E+2", 100, true},
		{"1.5e1", 15, true},
		{"1.5e0", 0, false},
		{"15e-1", 0, false},
		{"10e-1", 1, true},
		{"100e-2", 1, true},
		{"1e-1", 0, false},
		{"1e-400", 0, false},
		{"0.1e1", 1, true},
		{"0.001e3", 1, true},
		{"1230e-1", 123, true},
		{"1e18", 1000000000000000000, true},
		{"1e19", 0, false},
		{"9223372036854775807", 9223372036854775807, true},
		{"9223372036854775808", 0, false},
		{"-9223372036854775808", -9223372036854775808, true},
		{"-9223372036854775809", 0, false},
		{"922337203685477580.7e1", 9223372036854775807, true},
		{"92233720368547758070e-1", 9223372036854775807, true},
		{"1e99999999999999999999", 0, false},
		{"1e-99999999999999999999", 0, false},
		{"12345678901234567890123", 0, false},
		{"0.00000000000000000000000000000001e32", 1, true},
		{"1e0000000000000000000000000000000000000000000000000000000000002", 100, true},
	}
	for _, tt := range tests {
		v := &Value{Kind: Number, Number: tt.in}
		if n, ok := v.Int(); n != tt.want || ok != tt.ok {
			t.Errorf("Int of %s = %d, %v; want %d, %v", tt.in, n, ok, tt.want, tt.ok)
		}
	}
	for _, v := range []Value{{}, {Kind: String, String: "3"}, {Kind: Bool, Bool: true}, {Kind: Array}} {
		if n, ok := v.Int(); ok || n != 0 {
			t.Errorf("Int of a %v = %d, %v; want false", v.Kind, n, ok)
		}
	}
}

func TestFloat(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0", 0, true},
		{"-1.25", -1.25, true},
		{"1e2", 100, true},
		{"1e-400", 0, true},
		{"1e400", 0, false},
		{"-1e400", 0, false},
		{"1.7976931348623157e308", 1.7976931348623157e308, true},
	}
	for _, tt := range tests {
		v := &Value{Kind: Number, Number: tt.in}
		if x, ok := v.Float(); x != tt.want || ok != tt.ok {
			t.Errorf("Float of %s = %v, %v; want %v, %v", tt.in, x, ok, tt.want, tt.ok)
		}
	}
}
