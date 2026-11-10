package wpformat

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

var parseAttrsTests = []struct {
	name, in string
	want     []Attr
}{
	{"empty", "", nil},
	{"white space only", " \t\n\v\f\r ", nil},
	{"double quotes", `a="x y"`, []Attr{{"a", "x y"}}},
	{"single quotes", `a='x y'`, []Attr{{"a", "x y"}}},
	{"no quotes", `a=x`, []Attr{{"a", "x"}}},
	{"empty values", `a="" b='' c=`, []Attr{{"a", ""}, {"b", ""}, {"", "c="}}},
	{"white space around equals", ` a = b  c= "d" `, []Attr{{"a", "b"}, {"c", "d"}}},
	{"white space of all kinds", "a=1\tb=2\nc=3\rd=4\fe=5\vf=6", []Attr{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"d", "4"}, {"e", "5"}, {"f", "6"}}},
	{"a quoted value that has the white space", "a=\"x\ny\tz\"", []Attr{{"a", "x\ny\tz"}}},
	{"names with hyphens and underscores", `-a-=1 _b_=2`, []Attr{{"-a-", "1"}, {"_b_", "2"}}},
	{"names in lower case", `A=1 B="x" Cd='y'`, []Attr{{"a", "1"}, {"b", "x"}, {"cd", "y"}}},
	{"names with digits", `a1=1 2b=2`, []Attr{{"a1", "1"}, {"2b", "2"}}},
	{"values that have no name", `"p" 'q' r`, []Attr{{"", "p"}, {"", "q"}, {"", "r"}}},
	{"empty quoted values that have no name", `"" '' a=1`, []Attr{{"a", "1"}}},
	{"zero as a value that has no name", `"0" 0`, []Attr{{"", "0"}, {"", "0"}}},
	{"zero as a name", `0="x" 0=y 0='z' a="w"`, []Attr{{"a", "w"}}},
	{"equals in a value", `a=b=c`, []Attr{{"a", "b=c"}}},
	{"equals alone", `=`, []Attr{{"", "="}}},
	{"a name with no value", `a=`, []Attr{{"", "a="}}},
	{"a name with white space and no value", `a= `, []Attr{{"", "a="}}},
	{"a name, white space, and equals", `a =`, []Attr{{"", "a"}, {"", "="}}},
	{"mixed", `a='x' b="y" c=z 'q' "r" s`, []Attr{{"a", "x"}, {"b", "y"}, {"c", "z"}, {"", "q"}, {"", "r"}, {"", "s"}}},
	{"a quoted value with no white space after it", `a="x"b="y"`, []Attr{{"", `a="x"b="y"`}}},
	{"a quoted value with a letter after it", `a="x"y z`, []Attr{{"", `a="x"y`}, {"", "z"}}},
	{"a single quoted value with a letter after it", `a='x'y`, []Attr{{"", `a='x'y`}}},
	{"a quote in a value with no quotes", `a=b"c d`, []Attr{{"", `a=b"c`}, {"", "d"}}},
	{"a quote that does not end", `a="x y`, []Attr{{"", `a="x`}, {"", "y"}}},
	{"a quote that does not end, with no name", `"x y`, []Attr{{"", `"x`}, {"", "y"}}},
	{"a quote after the quoted value with no name", `"x"y`, []Attr{{"", `"x"y`}}},
	{"a quoted value of one quote kind in another", `a="it's" b='say "hi"'`, []Attr{{"a", "it's"}, {"b", `say "hi"`}}},
	{"an escaped quote", `a="x\"y" z`, []Attr{{"", `a="x"y"`}, {"", "z"}}},
	{"a value in the Japanese", `title="お問い合わせ" 日本語`, []Attr{{"title", "お問い合わせ"}, {"", "日本語"}}},
	{"the old form of Contact Form 7", `123 "お問い合わせ"`, []Attr{{"", "123"}, {"", "お問い合わせ"}}},
	{"a name that repeats", `a=1 a=2 b=3 a=4`, []Attr{{"a", "4"}, {"b", "3"}}},
	{"a name that repeats in another case", `a=1 A="2"`, []Attr{{"a", "2"}}},
	{"values with no name, after others", `a=1 x a=2 y`, []Attr{{"a", "2"}, {"", "x"}, {"", "y"}}},

	{"an escape of a letter", `a=\t\r\a\v\b\f\n`, []Attr{{"a", "\t\r\a\v\b\f\n"}}},
	{"an escape of a backslash", `a=\\`, []Attr{{"a", `\`}}},
	{"a backslash that ends the value", `a=\`, []Attr{{"a", `\`}}},
	{"a backslash in a quoted value that ends it", `a="\\"`, []Attr{{"a", `\`}}},
	{"an escape of another letter", `a=\q`, []Attr{{"a", "q"}}},
	{"an escape of a digit that is not octal", `a=\8`, []Attr{{"a", "8"}}},
	{"an escape of x, with no digits", `a=\x`, []Attr{{"a", "x"}}},
	{"an escape of x, with no hex digit", `a=\xg`, []Attr{{"a", "xg"}}},
	{"an escape of x, with one hex digit", `a=\x4`, []Attr{{"a", "\x04"}}},
	{"an escape of x, with one hex digit and a letter", `a=\x4g`, []Attr{{"a", "\x04g"}}},
	{"an escape of x, with two hex digits", `a=\x41`, []Attr{{"a", "A"}}},
	{"an escape of x, with three hex digits", `a=\x414`, []Attr{{"a", "A4"}}},
	{"an escape of x, with capital hex digits", `a=\xA1`, []Attr{{"a", "\xa1"}}},
	{"an escape of octal digits", `a=\101`, []Attr{{"a", "A"}}},
	{"an escape of four octal digits", `a=\1018`, []Attr{{"a", "A8"}}},
	{"an escape of two octal digits", `a=\41z`, []Attr{{"a", "!z"}}},
	{"an escape of octal digits that is over a byte", `a=\777`, []Attr{{"a", "\xff"}}},
	{"an escape of zero", `a=\0`, []Attr{{"a", "\x00"}}},
	{"escapes in a value that has no name", `\x41\n`, []Attr{{"", "A\n"}}},
	{"an escape in a quoted value that has no name", `"\101"`, []Attr{{"", "A"}}},

	{"a no-break space between attributes", "a=1\xc2\xa0b=2", []Attr{{"a", "1"}, {"b", "2"}}},
	{"a run of no-break and zero-width spaces", "a=1\xe2\x80\x8b\xc2\xa0\xe2\x80\x8bb=2", []Attr{{"a", "1"}, {"b", "2"}}},
	{"a no-break space in a quoted value", "a=\"x\xc2\xa0\xc2\xa0y\"", []Attr{{"a", "x y"}}},
	{"no-break spaces around", "\xc2\xa0a=1\xc2\xa0", []Attr{{"a", "1"}}},
	{"a zero-width space only", "\xe2\x80\x8b", nil},
	{"a byte that is not UTF-8", "\xff a=1", nil},
	{"a byte that is not UTF-8 in a value", "a=\"\xc3\"", nil},
	{"a surrogate", "a=\xed\xa0\x80", nil},
	{"a letter with a mark, as a value", "a=e\xcc\x81", []Attr{{"a", "e\xcc\x81"}}},

	{"a value with an element", `a="<b>" c="<b" d="x>y" e="<<>"`, []Attr{{"a", "<b>"}, {"c", ""}, {"d", "x>y"}, {"e", "<<>"}}},
	{"a value that has an element start and no end", `a=<b`, []Attr{{"a", ""}}},
	{"a value with two elements and a start", `a="<b>x</b><i"`, []Attr{{"a", ""}}},
	{"a value with a start before an end", `a="<b <i>"`, []Attr{{"a", "<b <i>"}}},
	{"an element as a value that has no name", `<b>`, []Attr{{"", "<b>"}}},
	{"an element start as a value that has no name", `<b x`, []Attr{{"", ""}, {"", "x"}}},
	{"a start made by an escape", `a=\x3cb`, []Attr{{"a", ""}}},
	{"an element made by escapes", `a=\x3cb\x3e`, []Attr{{"a", "<b>"}}},
}

func TestParseAttrs(t *testing.T) {
	for _, tt := range parseAttrsTests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAttrs(tt.in)
			if !slices.Equal(got, AttrList(tt.want)) {
				t.Errorf("ParseAttrs(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if plain := referenceAttrs(tt.in); !slices.Equal(got, plain) {
				t.Errorf("ParseAttrs(%q) = %q, and the plain search made %q", tt.in, got, plain)
			}
		})
	}
}

func TestParseAttrsMany(t *testing.T) {
	// Past attrScanLimit attributes, the names are looked up in a map. The number does not come from the limit, so that a
	// change of the limit cannot make the text huge.
	n := 48
	if n <= attrScanLimit {
		t.Fatalf("the text has %d attributes, which does not pass the limit %d: make the text longer", n, attrScanLimit)
	}
	var in strings.Builder
	for i := range n {
		fmt.Fprintf(&in, "n%d=v%d ", i, i)
	}
	in.WriteString(`n2=new "x" n20=again N39=last "x"`)
	got := ParseAttrs(in.String())
	var want []Attr
	for i := range n {
		want = append(want, Attr{fmt.Sprintf("n%d", i), fmt.Sprintf("v%d", i)})
	}
	want[2].Value, want[20].Value, want[39].Value = "new", "again", "last"
	want = append(want, Attr{"", "x"}, Attr{"", "x"})
	if !slices.Equal(got, AttrList(want)) {
		t.Errorf("ParseAttrs of %d attributes made %d, want %d, with new values", n, len(got), len(want))
		for i := range min(len(got), len(want)) {
			if got[i] != want[i] {
				t.Errorf("the first difference is at %d: %q, want %q", i, got[i], want[i])
				break
			}
		}
	}
}

func TestAttrList(t *testing.T) {
	as := ParseAttrs(`123 id="a" "Title" ID=b`)
	if v, ok := as.Get("id"); !ok || v != "b" {
		t.Errorf("Get(id) = %q, %v, want b, true", v, ok)
	}
	if v, ok := as.Get("ID"); ok {
		t.Errorf("Get(ID) = %q, true, want no value: names are in lower case", v)
	}
	if v, ok := as.Get(""); ok {
		t.Errorf("Get(\"\") = %q, true, want no value", v)
	}
	if v, ok := as.Get("title"); ok {
		t.Errorf("Get(title) = %q, true, want no value", v)
	}
	if a, ok := as.First(); !ok || a != (Attr{"", "123"}) {
		t.Errorf("First() = %q, %v, want the first value", a, ok)
	}
	if a, ok := AttrList(nil).First(); ok {
		t.Errorf("First() of no attributes = %q, true", a)
	}
	if v, ok := AttrList(nil).Get("a"); ok {
		t.Errorf("Get(a) of no attributes = %q, true", v)
	}
}
