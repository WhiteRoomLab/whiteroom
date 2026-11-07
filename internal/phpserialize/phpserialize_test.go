package phpserialize

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var null = Value{Kind: Null}

func boolean(b bool) Value    { return Value{Kind: Bool, Bool: b} }
func integer(n int64) Value   { return Value{Kind: Int, Int: n} }
func float(f float64) Value   { return Value{Kind: Float, Float: f} }
func str(s string) Value      { return Value{Kind: String, Str: s} }
func array(es ...Entry) Value { return Value{Kind: Array, Array: es} }

func field(name string, v Value) Entry {
	return Entry{Key: str(name), Value: v}
}

// list returns an array with the integer keys 0, 1, and so on.
func list(vs ...Value) Value {
	es := make([]Entry, len(vs))
	for i, v := range vs {
		es[i] = Entry{Key: integer(int64(i)), Value: v}
	}
	return array(es...)
}

// ps returns s as PHP serializes a string, so that the length of a text need not be counted by hand.
func ps(s string) string {
	return fmt.Sprintf("s:%d:\"%s\";", len(s), s)
}

// serialize writes v as PHP's serialize() does, for the values that Unserialize reads.
func serialize(v Value) string {
	var b strings.Builder
	write(&b, v)
	return b.String()
}

func write(b *strings.Builder, v Value) {
	switch v.Kind {
	case Null:
		b.WriteString("N;")
	case Bool:
		if v.Bool {
			b.WriteString("b:1;")
		} else {
			b.WriteString("b:0;")
		}
	case Int:
		fmt.Fprintf(b, "i:%d;", v.Int)
	case Float:
		fmt.Fprintf(b, "d:%s;", formatFloat(v.Float))
	case String:
		b.WriteString(ps(v.Str))
	case Array:
		fmt.Fprintf(b, "a:%d:{", len(v.Array))
		for _, e := range v.Array {
			write(b, e.Key)
			write(b, e.Value)
		}
		b.WriteByte('}')
	}
}

func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NAN"
	case math.IsInf(f, 1):
		return "INF"
	case math.IsInf(f, -1):
		return "-INF"
	}
	return strconv.FormatFloat(f, 'G', -1, 64)
}

// equalValue compares the bits of floats, so that -0 differs from 0, and takes all NaNs as equal.
func equalValue(a, b Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case Null:
		return true
	case Bool:
		return a.Bool == b.Bool
	case Int:
		return a.Int == b.Int
	case Float:
		return math.Float64bits(a.Float) == math.Float64bits(b.Float) || (math.IsNaN(a.Float) && math.IsNaN(b.Float))
	case String:
		return a.Str == b.Str
	case Array:
		return slices.EqualFunc(a.Array, b.Array, func(x, y Entry) bool {
			return equalValue(x.Key, y.Key) && equalValue(x.Value, y.Value)
		})
	}
	return false
}

// wellFormed reports whether only the field for Kind is set, and whether the keys are Ints or Strings.
func wellFormed(v Value) bool {
	switch v.Kind {
	case Null:
		return !v.Bool && v.Int == 0 && v.Float == 0 && v.Str == "" && v.Array == nil
	case Bool:
		return v.Int == 0 && v.Float == 0 && v.Str == "" && v.Array == nil
	case Int:
		return !v.Bool && v.Float == 0 && v.Str == "" && v.Array == nil
	case Float:
		return !v.Bool && v.Int == 0 && v.Str == "" && v.Array == nil
	case String:
		return !v.Bool && v.Int == 0 && v.Float == 0 && v.Array == nil
	case Array:
		if v.Bool || v.Int != 0 || v.Float != 0 || v.Str != "" {
			return false
		}
		for _, e := range v.Array {
			if (e.Key.Kind != Int && e.Key.Kind != String) || !wellFormed(e.Key) || !wellFormed(e.Value) {
				return false
			}
		}
		return true
	}
	return false
}

// depth returns the number of levels of arrays in v.
func depth(v Value) int {
	if v.Kind != Array {
		return 0
	}
	d := 0
	for _, e := range v.Array {
		d = max(d, depth(e.Value))
	}
	return d + 1
}

// nested returns a value with n arrays, one in the other.
func nested(n int) string {
	return strings.Repeat("a:1:{i:0;", n) + "N;" + strings.Repeat("}", n)
}

const (
	mailSubject = `[_site_title] お問い合わせ「[your-subject]」`
	mailSender  = `[_site_title] <wordpress@example.com>`
	mailBody    = "差出人: [your-name] <[your-email]>\n件名: [your-subject]\n\nメッセージ本文:\n[your-message]\n\n-- \n" +
		"このメールは [_site_title] ([_site_url]) のお問い合わせフォームから送信されました"
)

const attachmentMetadata = `a:7:{s:5:"width";i:2560;s:6:"height";i:1440;s:4:"file";s:26:"2026/11/example-scaled.jpg";` +
	`s:8:"filesize";i:123456;s:5:"sizes";a:2:{` +
	`s:6:"medium";a:5:{s:4:"file";s:19:"example-300x169.jpg";s:5:"width";i:300;s:6:"height";i:169;` +
	`s:9:"mime-type";s:10:"image/jpeg";s:8:"filesize";i:12345;}` +
	`s:9:"thumbnail";a:5:{s:4:"file";s:19:"example-150x150.jpg";s:5:"width";i:150;s:6:"height";i:150;` +
	`s:9:"mime-type";s:10:"image/jpeg";s:8:"filesize";i:5432;}}` +
	`s:10:"image_meta";a:12:{s:8:"aperture";s:1:"0";s:6:"credit";s:0:"";s:6:"camera";s:0:"";s:7:"caption";s:0:"";` +
	`s:17:"created_timestamp";s:1:"0";s:9:"copyright";s:0:"";s:12:"focal_length";s:1:"0";s:3:"iso";s:1:"0";` +
	`s:13:"shutter_speed";s:1:"0";s:5:"title";s:0:"";s:11:"orientation";s:1:"0";s:8:"keywords";a:0:{}}` +
	`s:14:"original_image";s:11:"example.jpg";}`

var unserializeTests = []struct {
	name string
	in   string
	want Value
}{
	{name: "null", in: "N;", want: null},
	{name: "false", in: "b:0;", want: boolean(false)},
	{name: "true", in: "b:1;", want: boolean(true)},
	{name: "zero", in: "i:0;", want: integer(0)},
	{name: "negative integer", in: "i:-12;", want: integer(-12)},
	{name: "plus sign", in: "i:+5;", want: integer(5)},
	{name: "minus zero", in: "i:-0;", want: integer(0)},
	{name: "leading zeros", in: "i:007;", want: integer(7)},
	{name: "largest int64", in: "i:9223372036854775807;", want: integer(math.MaxInt64)},
	{name: "smallest int64", in: "i:-9223372036854775808;", want: integer(math.MinInt64)},
	{name: "float", in: "d:0.5;", want: float(0.5)},
	{name: "float without a point", in: "d:5;", want: float(5)},
	{name: "negative zero float", in: "d:-0;", want: float(math.Copysign(0, -1))},
	{name: "exponent", in: "d:1.0E+25;", want: float(1e25)},
	{name: "lowercase exponent", in: "d:1e5;", want: float(100000)},
	{name: "negative exponent", in: "d:-1.5E-3;", want: float(-0.0015)},
	{name: "no integer part", in: "d:.5;", want: float(0.5)},
	{name: "no fraction", in: "d:5.;", want: float(5)},
	{name: "no fraction before the exponent", in: "d:5.e2;", want: float(500)},
	{name: "plus sign in a float", in: "d:+0.25;", want: float(0.25)},
	{name: "17 digits", in: "d:0.10000000000000001;", want: float(0.1)},
	{name: "below the smallest float", in: "d:1e-400;", want: float(0)},
	{name: "infinity", in: "d:INF;", want: float(math.Inf(1))},
	{name: "negative infinity", in: "d:-INF;", want: float(math.Inf(-1))},
	{name: "not a number", in: "d:NAN;", want: float(math.NaN())},
	{name: "empty string", in: `s:0:"";`, want: str("")},
	{name: "string", in: `s:5:"hello";`, want: str("hello")},
	{name: "leading zeros in the length", in: `s:005:"hello";`, want: str("hello")},
	{name: "quote and semicolon", in: `s:4:"a";b";`, want: str(`a";b`)},
	{name: "newline", in: "s:3:\"a\nb\";", want: str("a\nb")},
	{name: "Japanese", in: `s:9:"日本語";`, want: str("日本語")},
	{name: "not UTF-8", in: "s:3:\"\xff\xfe\x00\";", want: str("\xff\xfe\x00")},
	{name: "serialized text in a string", in: `s:6:"a:0:{}";`, want: str("a:0:{}")},
	{name: "empty array", in: "a:0:{}", want: array()},
	{name: "list", in: `a:2:{i:0;s:1:"a";i:1;s:1:"b";}`, want: list(str("a"), str("b"))},
	{
		name: "keys in the written order",
		in:   `a:3:{s:1:"b";i:1;i:7;i:2;s:1:"a";i:3;}`,
		want: array(field("b", integer(1)), Entry{Key: integer(7), Value: integer(2)}, field("a", integer(3))),
	},
	{name: "negative key", in: "a:1:{i:-1;N;}", want: array(Entry{Key: integer(-1), Value: null})},
	{name: "numeric string key as written", in: `a:1:{s:1:"5";s:1:"x";}`, want: array(field("5", str("x")))},
	{
		name: "keys that are not canonical integers",
		in:   `a:5:{s:2:"-0";N;s:2:"05";N;s:2:" 5";N;i:5;N;s:19:"9223372036854775808";N;}`,
		want: array(
			field("-0", null), field("05", null), field(" 5", null),
			Entry{Key: integer(5), Value: null}, field("9223372036854775808", null),
		),
	},
	{name: "nested arrays", in: "a:2:{i:0;a:1:{i:0;N;}i:1;a:0:{}}", want: list(list(null), array())},
	{
		name: "attachment metadata",
		in:   attachmentMetadata,
		want: array(
			field("width", integer(2560)),
			field("height", integer(1440)),
			field("file", str("2026/11/example-scaled.jpg")),
			field("filesize", integer(123456)),
			field("sizes", array(
				field("medium", array(
					field("file", str("example-300x169.jpg")),
					field("width", integer(300)),
					field("height", integer(169)),
					field("mime-type", str("image/jpeg")),
					field("filesize", integer(12345)),
				)),
				field("thumbnail", array(
					field("file", str("example-150x150.jpg")),
					field("width", integer(150)),
					field("height", integer(150)),
					field("mime-type", str("image/jpeg")),
					field("filesize", integer(5432)),
				)),
			)),
			field("image_meta", array(
				field("aperture", str("0")),
				field("credit", str("")),
				field("camera", str("")),
				field("caption", str("")),
				field("created_timestamp", str("0")),
				field("copyright", str("")),
				field("focal_length", str("0")),
				field("iso", str("0")),
				field("shutter_speed", str("0")),
				field("title", str("")),
				field("orientation", str("0")),
				field("keywords", array()),
			)),
			field("original_image", str("example.jpg")),
		),
	},
	{
		name: "Contact Form 7 mail",
		in: "a:9:{" + ps("active") + "b:1;" + ps("subject") + ps(mailSubject) + ps("sender") + ps(mailSender) +
			ps("recipient") + ps("[_site_admin_email]") + ps("body") + ps(mailBody) +
			ps("additional_headers") + ps("Reply-To: [your-email]") + ps("attachments") + ps("") +
			ps("use_html") + "b:0;" + ps("exclude_blank") + "b:0;}",
		want: array(
			field("active", boolean(true)),
			field("subject", str(mailSubject)),
			field("sender", str(mailSender)),
			field("recipient", str("[_site_admin_email]")),
			field("body", str(mailBody)),
			field("additional_headers", str("Reply-To: [your-email]")),
			field("attachments", str("")),
			field("use_html", boolean(false)),
			field("exclude_blank", boolean(false)),
		),
	},
	{name: "menu item classes", in: `a:1:{i:0;s:0:"";}`, want: list(str(""))},
	{
		name: "Polylang language",
		in:   `a:4:{s:6:"locale";s:5:"ja_JP";s:3:"rtl";i:0;s:10:"term_group";i:0;s:9:"flag_code";s:2:"jp";}`,
		want: array(
			field("locale", str("ja_JP")),
			field("rtl", integer(0)),
			field("term_group", integer(0)),
			field("flag_code", str("jp")),
		),
	},
}

func TestUnserialize(t *testing.T) {
	for _, tt := range unserializeTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Unserialize(tt.in)
			if err != nil {
				t.Fatalf("Unserialize(%q) = %v", tt.in, err)
			}
			if !wellFormed(got) {
				t.Errorf("Unserialize(%q) = %+v; want only the field for Kind set", tt.in, got)
			}
			if !equalValue(got, tt.want) {
				t.Errorf("Unserialize(%q) = %s; want %s", tt.in, serialize(got), serialize(tt.want))
			}
		})
	}
}

func TestUnserializeAttachmentMetadata(t *testing.T) {
	v, err := Unserialize(attachmentMetadata)
	if err != nil {
		t.Fatal(err)
	}
	sizes, _ := v.Lookup("sizes")
	thumbnail, _ := sizes.Lookup("thumbnail")
	file, ok := thumbnail.Lookup("file")
	if !ok || file.Kind != String || file.Str != "example-150x150.jpg" {
		t.Errorf("sizes.thumbnail.file = %+v, %v; want example-150x150.jpg", file, ok)
	}
	original, ok := v.Lookup("original_image")
	if !ok || original.Str != "example.jpg" {
		t.Errorf("original_image = %+v, %v; want example.jpg", original, ok)
	}
}

func TestUnserializeTruncated(t *testing.T) {
	for _, tt := range unserializeTests {
		t.Run(tt.name, func(t *testing.T) {
			for i := range len(tt.in) {
				prefix := tt.in[:i]
				_, err := Unserialize(prefix)
				var e *Error
				if !errors.As(err, &e) || e.Offset < 0 || e.Offset > len(prefix) {
					t.Errorf("Unserialize(%q) = %v; want an *Error in the input", prefix, err)
				}
			}
			_, err := Unserialize(tt.in + "x")
			var e *Error
			if !errors.As(err, &e) || e.Offset != len(tt.in) || e.Msg != msgExtra {
				t.Errorf("Unserialize(%q) = %v; want byte %d: %s", tt.in+"x", err, len(tt.in), msgExtra)
			}
		})
	}
}

func TestUnserializeLargeArray(t *testing.T) {
	const n = 20000
	var b strings.Builder
	fmt.Fprintf(&b, "a:%d:{", n)
	for i := range n {
		fmt.Fprintf(&b, "i:%d;i:%d;", n-i, i)
	}
	b.WriteString("}")
	v, err := Unserialize(b.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Array) != n {
		t.Fatalf("len = %d, want %d", len(v.Array), n)
	}
	for i, e := range v.Array {
		if e.Key.Int != int64(n-i) || e.Value.Int != int64(i) {
			t.Fatalf("entry %d = %v, %v; want %d, %d", i, e.Key.Int, e.Value.Int, n-i, i)
		}
	}
}

// TestUnserializeAllocation checks that the size that an array claims does not decide how much is allocated.
func TestUnserializeAllocation(t *testing.T) {
	const claim = "a:1000000:{"
	in := claim + strings.Repeat("x", MaxBytes-len(claim))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := Unserialize(in)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatalf("Unserialize(%d bytes) succeeded, want an error", len(in))
	}
	got := after.TotalAlloc - before.TotalAlloc
	t.Logf("allocated %d KiB for %d bytes", got>>10, len(in))
	if got > 64<<20 {
		t.Errorf("Unserialize(%d bytes) allocated %d MiB, want at most 64 MiB", len(in), got>>20)
	}
}

const (
	msgEOF    = "unexpected end of the value"
	msgExtra  = "unexpected data after the value"
	msgKey    = "array keys must be integers or strings"
	msgDup    = "duplicate key"
	msgObject = "objects are not supported"
	msgDigit  = "expected a digit, found "
	msgLen    = "string is longer than the rest of the input"
)

var unserializeErrorTests = []struct {
	name string
	in   string
	off  int
	msg  string
}{
	{name: "empty input", in: "", off: 0, msg: msgEOF},
	{name: "null without a semicolon", in: "N", off: 1, msg: msgEOF},
	{name: "null followed by a letter", in: "Nx", off: 1, msg: "expected ';', found 'x'"},
	{name: "boolean 2", in: "b:2;", off: 2, msg: "expected 0 or 1, found '2'"},
	{name: "boolean 10", in: "b:10;", off: 3, msg: "expected ';', found '0'"},
	{name: "boolean without a value", in: "b:", off: 2, msg: msgEOF},
	{name: "boolean without a colon", in: "b0;", off: 1, msg: "expected ':', found '0'"},
	{name: "integer without digits", in: "i:;", off: 2, msg: msgDigit + "';'"},
	{name: "integer with a sign only", in: "i:-;", off: 3, msg: msgDigit + "';'"},
	{name: "integer with a space", in: "i: 1;", off: 2, msg: msgDigit + "' '"},
	{name: "integer with a fraction", in: "i:1.5;", off: 3, msg: "expected ';', found '.'"},
	{name: "integer with a space before the semicolon", in: "i:0 ;", off: 3, msg: "expected ';', found ' '"},
	{name: "integer without a semicolon", in: "i:5", off: 3, msg: msgEOF},
	{name: "integer above int64", in: "i:9223372036854775808;", off: 2, msg: "integer is out of the int64 range"},
	{name: "integer below int64", in: "i:-9223372036854775809;", off: 2, msg: "integer is out of the int64 range"},
	{name: "float exponent without digits", in: "d:1e;", off: 4, msg: msgDigit + "';'"},
	{name: "float exponent with a sign only", in: "d:1e+;", off: 5, msg: msgDigit + "';'"},
	{name: "float in hexadecimal", in: "d:0x10;", off: 3, msg: "expected ';', found 'x'"},
	{name: "float Infinity", in: "d:Infinity;", off: 2, msg: msgDigit + "'I'"},
	{name: "float +INF", in: "d:+INF;", off: 3, msg: msgDigit + "'I'"},
	{name: "float inf", in: "d:inf;", off: 2, msg: msgDigit + "'i'"},
	{name: "float nan", in: "d:nan;", off: 2, msg: msgDigit + "'n'"},
	{name: "float -NAN", in: "d:-NAN;", off: 3, msg: msgDigit + "'N'"},
	{name: "float with an underscore", in: "d:1_0;", off: 3, msg: "expected ';', found '_'"},
	{name: "float with a point only", in: "d:.;", off: 3, msg: msgDigit + "';'"},
	{name: "float with a point in the exponent", in: "d:1e1.5;", off: 5, msg: "expected ';', found '.'"},
	{name: "float above the range", in: "d:1e999;", off: 2, msg: "float is out of range"},
	{name: "float INF followed by a letter", in: "d:INFx;", off: 5, msg: "expected ';', found 'x'"},
	{name: "float without a value", in: "d:", off: 2, msg: msgEOF},
	{name: "float without a semicolon", in: "d:0.5", off: 5, msg: msgEOF},
	{name: "string longer than the input", in: `s:5:"abc";`, off: 10, msg: msgEOF},
	{name: "string without the closing semicolon", in: `s:3:"abc"`, off: 9, msg: msgEOF},
	{name: "string longer than declared", in: `s:3:"abcd";`, off: 8, msg: `expected '"', found 'd'`},
	{name: "string with a negative length", in: `s:-1:"";`, off: 2, msg: msgDigit + "'-'"},
	{name: "string without a length", in: `s::"";`, off: 2, msg: msgDigit + "':'"},
	{name: "string with a huge length", in: `s:999999999:"a";`, off: 2, msg: msgLen},
	{name: "string with a length beyond int64", in: `s:99999999999999999999:"a";`, off: 2, msg: msgLen},
	{name: "string with a length at 2 to the 63", in: `s:9223372036854775808:"a";`, off: 2, msg: msgLen},
	{name: "string with a length at 2 to the 64 plus 1", in: `s:18446744073709551617:"a";`, off: 2, msg: msgLen},
	{name: "string without the opening quote", in: `s:3:abc";`, off: 4, msg: `expected '"', found 'a'`},
	{name: "array with fewer elements", in: "a:2:{i:0;i:1;}", off: 13, msg: "array has fewer elements than declared"},
	{name: "array with an empty body", in: "a:1:{}", off: 5, msg: "array has fewer elements than declared"},
	{name: "array with more elements", in: "a:1:{i:0;i:1;i:2;i:3;}", off: 13, msg: "expected '}', found 'i'"},
	{name: "array without the closing brace", in: "a:1:{i:0;i:1;", off: 13, msg: msgEOF},
	{name: "array without the opening brace", in: "a:0:}", off: 4, msg: "expected '{', found '}'"},
	{name: "array without a count", in: "a::{}", off: 2, msg: msgDigit + "':'"},
	{name: "array with a huge count", in: "a:999999999:{", off: 13, msg: msgEOF},
	{name: "array with a count at 2 to the 64", in: "a:18446744073709551616:{}", off: 24, msg: "array has fewer elements than declared"},
	{name: "array with a null key", in: "a:1:{N;i:1;}", off: 5, msg: msgKey},
	{name: "array with an array key", in: "a:1:{a:0:{}i:1;}", off: 5, msg: msgKey},
	{name: "array with a float key", in: "a:1:{d:1.5;i:1;}", off: 5, msg: msgKey},
	{name: "array with a boolean key", in: "a:1:{b:1;i:1;}", off: 5, msg: msgKey},
	{name: "array keys that overlap", in: `a:2:{i:5;i:1;s:1:"5";i:2;}`, off: len(`a:2:{i:5;i:1;`), msg: msgDup},
	{name: "array with a repeated integer key", in: "a:2:{i:5;i:1;i:5;i:2;}", off: len("a:2:{i:5;i:1;"), msg: msgDup},
	{
		name: "array with a repeated string key",
		in:   `a:2:{s:1:"a";i:1;s:1:"a";i:2;}`,
		off:  len(`a:2:{s:1:"a";i:1;`),
		msg:  msgDup,
	},
	{name: `array keys 0 and "0"`, in: `a:2:{s:1:"0";N;i:0;N;}`, off: len(`a:2:{s:1:"0";N;`), msg: msgDup},
	{name: `array keys -5 and "-5"`, in: `a:2:{i:-5;N;s:2:"-5";N;}`, off: len("a:2:{i:-5;N;"), msg: msgDup},
	{
		name: "array keys at the top of int64",
		in:   `a:2:{i:9223372036854775807;N;s:19:"9223372036854775807";N;}`,
		off:  len("a:2:{i:9223372036854775807;N;"),
		msg:  msgDup,
	},
	{
		name: "array keys at the bottom of int64",
		in:   `a:2:{s:20:"-9223372036854775808";N;i:-9223372036854775808;N;}`,
		off:  len(`a:2:{s:20:"-9223372036854775808";N;`),
		msg:  msgDup,
	},
	{name: "object", in: `O:8:"stdClass":0:{}`, off: 0, msg: msgObject},
	{name: "serialized object", in: `C:3:"Foo":0:{}`, off: 0, msg: msgObject},
	{name: "object of PHP 3", in: "o:0:{}", off: 0, msg: msgObject},
	{name: "object in an array", in: `a:1:{i:0;O:8:"stdClass":0:{}}`, off: len("a:1:{i:0;"), msg: msgObject},
	{name: "enum", in: `E:7:"Foo:Bar";`, off: 0, msg: "enums are not supported"},
	{name: "reference", in: "r:1;", off: 0, msg: "references are not supported"},
	{name: "reference to a value", in: "R:1;", off: 0, msg: "references are not supported"},
	{name: "escaped string", in: `S:1:"a";`, off: 0, msg: "escaped strings are not supported"},
	{name: "unknown type", in: "x:0;", off: 0, msg: "unexpected 'x'"},
	{name: "digit", in: "5", off: 0, msg: "unexpected '5'"},
	{name: "byte beyond ASCII", in: "\xff", off: 0, msg: `unexpected '\xff'`},
	{name: "data after the value", in: "i:0;i:1;", off: 4, msg: msgExtra},
	{name: "leading space", in: " i:0;", off: 0, msg: "unexpected ' '"},
	{name: "trailing space", in: "i:0; ", off: 4, msg: msgExtra},
	{name: "trailing newline", in: "i:0;\n", off: 4, msg: msgExtra},
	{name: "error in a nested value", in: "a:1:{i:0;a:1:{i:0;i:x;}}", off: len("a:1:{i:0;a:1:{i:0;i:"), msg: msgDigit + "'x'"},
	{name: "end in a nested value", in: "a:1:{i:0;a:1:{", off: 14, msg: msgEOF},
}

func TestUnserializeErrors(t *testing.T) {
	for _, tt := range unserializeErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Unserialize(tt.in)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Unserialize(%q) = %s, %v; want *Error", tt.in, serialize(got), err)
			}
			if e.Offset != tt.off || e.Msg != tt.msg {
				t.Errorf("Unserialize(%q) = %v; want byte %d: %s", tt.in, err, tt.off, tt.msg)
			}
			if want := fmt.Sprintf("byte %d: %s", tt.off, tt.msg); err.Error() != want {
				t.Errorf("Unserialize(%q).Error() = %q; want %q", tt.in, err.Error(), want)
			}
			if !wellFormed(got) || got.Kind != Null {
				t.Errorf("Unserialize(%q) = %+v with an error; want the zero Value", tt.in, got)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		v, err := Unserialize(nested(MaxDepth))
		if err != nil {
			t.Fatalf("Unserialize(%d levels) = %v", MaxDepth, err)
		}
		if got := depth(v); got != MaxDepth {
			t.Errorf("depth = %d, want %d", got, MaxDepth)
		}
		_, err = Unserialize(nested(MaxDepth + 1))
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("Unserialize(%d levels) = %v; want *Error", MaxDepth+1, err)
		}
		off := MaxDepth * len("a:1:{i:0;")
		msg := fmt.Sprintf("arrays are nested too deeply: more than %d levels", MaxDepth)
		if e.Offset != off || e.Msg != msg {
			t.Errorf("Unserialize(%d levels) = %v; want byte %d: %s", MaxDepth+1, err, off, msg)
		}
	})
	t.Run("length", func(t *testing.T) {
		// s:<n>:"<n bytes>"; is n+13 bytes long when n has 7 digits.
		const n = MaxBytes - 13
		exact := ps(strings.Repeat("a", n))
		if len(exact) != MaxBytes {
			t.Fatalf("len = %d, want %d", len(exact), MaxBytes)
		}
		v, err := Unserialize(exact)
		if err != nil || len(v.Str) != n {
			t.Fatalf("Unserialize(%d bytes) = %d bytes, %v", MaxBytes, len(v.Str), err)
		}
		over := ps(strings.Repeat("a", n+1))
		_, err = Unserialize(over)
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("Unserialize(%d bytes) = %v; want *Error", len(over), err)
		}
		msg := fmt.Sprintf("too long: %d bytes (max %d)", MaxBytes+1, MaxBytes)
		if e.Offset != MaxBytes || e.Msg != msg {
			t.Errorf("Unserialize(%d bytes) = %v; want byte %d: %s", len(over), err, MaxBytes, msg)
		}
	})
}

func TestLooksSerialized(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"N;", true},
		{"b:1;", true},
		{"b:0;", true},
		{"i:5;", true},
		{"i:-5;", true},
		{"i:+5;", true},
		{"d:0.5;", true},
		{"d:1.0E+25;", true},
		{`s:1:"a";`, true},
		{`s:0:"";`, true},
		{"a:0:{}", true},
		{"a:1:{i:0;N;}", true},
		{`O:8:"stdClass":0:{}`, true},
		{`E:7:"Foo:Bar";`, true},
		{attachmentMetadata, true},
		{" \t\r\n\vN;", true},
		{"N;\x00", true},
		{"\n\ta:0:{}\r\n", true},
		// Only the shape counts.
		{"i:1.5;", true},
		{"b:2;", true},
		{`s:5:"ab";`, true},
		{"a:9:{x}", true},

		{"", false},
		{"a", false},
		{"N", false},
		{"n;", false},
		{"N;N;", false},
		{`s:1:"a"`, false},
		{`s:1:"a" ;`, false},
		{`s:x:"a";`, false},
		{"i:5", false},
		{"i:;", false},
		{"i:5;;", false},
		{"b:1;x;", false},
		{"x:0;", false},
		{"a:0:{", false},
		{"a:x:{}", false},
		{"O:x:{}", false},
		{"ab:0;", false},
		{"Hello, world", false},
		{"\fN;", false},
		// As in WordPress, d: does not accept INF, NAN, and a lowercase e.
		{"d:INF;", false},
		{"d:-INF;", false},
		{"d:NAN;", false},
		{"d:1e5;", false},
	} {
		if got := LooksSerialized(tt.in); got != tt.want {
			t.Errorf("LooksSerialized(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestLookup(t *testing.T) {
	v := array(
		Entry{Key: integer(0), Value: str("zero")},
		Entry{Key: integer(-3), Value: str("minus three")},
		Entry{Key: integer(math.MaxInt64), Value: str("largest")},
		field("5", str("five")),
		field("05", str("zero five")),
		field("-0", str("minus zero")),
		field(" 5", str("space five")),
		field("9223372036854775808", str("too large")),
		field("name", str("name")),
	)
	for _, tt := range []struct {
		key  string
		want string
		ok   bool
	}{
		{"0", "zero", true},
		{"-3", "minus three", true},
		{"9223372036854775807", "largest", true},
		{"5", "five", true},
		{"05", "zero five", true},
		{"-0", "minus zero", true},
		{" 5", "space five", true},
		{"9223372036854775808", "too large", true},
		{"name", "name", true},
		{"1", "", false},
		{"+5", "", false},
		{"-5", "", false},
		{"", "", false},
		{"Name", "", false},
	} {
		got, ok := v.Lookup(tt.key)
		if ok != tt.ok || got.Str != tt.want {
			t.Errorf("Lookup(%q) = %q, %v; want %q, %v", tt.key, got.Str, ok, tt.want, tt.ok)
		}
	}

	if got, ok := list(str("a"), str("b")).Lookup("1"); !ok || got.Str != "b" {
		t.Errorf("Lookup(%q) of a list = %q, %v; want b", "1", got.Str, ok)
	}
	if got, ok := array(Entry{Key: integer(5), Value: str("five")}).Lookup("5"); !ok || got.Str != "five" {
		t.Errorf("Lookup(%q) of the key 5 = %q, %v; want five", "5", got.Str, ok)
	}
	if got, ok := array(Entry{Key: integer(5), Value: str("five")}).Lookup("05"); ok {
		t.Errorf("Lookup(%q) of the key 5 = %q, %v; want none", "05", got.Str, ok)
	}
	for _, scalar := range []Value{null, boolean(true), integer(0), float(0), str("0")} {
		if got, ok := scalar.Lookup("0"); ok || !equalValue(got, null) {
			t.Errorf("Lookup(%q) of %s = %+v, %v; want the zero Value and false", "0", serialize(scalar), got, ok)
		}
	}
}

func TestLookupParsed(t *testing.T) {
	v, err := Unserialize(`a:3:{s:1:"5";s:1:"a";i:7;s:1:"b";s:2:"05";s:1:"c";}`)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"5": "a", "7": "b", "05": "c"} {
		if got, ok := v.Lookup(key); !ok || got.Str != want {
			t.Errorf("Lookup(%q) = %q, %v; want %q", key, got.Str, ok, want)
		}
	}
}

func TestSerialize(t *testing.T) {
	v := array(
		field("a", integer(-1)),
		Entry{Key: integer(2), Value: float(0.5)},
		field("日本", list(null, boolean(true), boolean(false), str("x"), float(math.Inf(-1)), float(math.NaN()))),
	)
	want := `a:3:{s:1:"a";i:-1;i:2;d:0.5;s:6:"日本";a:6:{i:0;N;i:1;b:1;i:2;b:0;i:3;s:1:"x";i:4;d:-INF;i:5;d:NAN;}}`
	if got := serialize(v); got != want {
		t.Errorf("serialize() = %s, want %s", got, want)
	}
}

func TestErrorString(t *testing.T) {
	err := &Error{Offset: 3, Msg: "unexpected end of the value"}
	if got, want := err.Error(), "byte 3: unexpected end of the value"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
