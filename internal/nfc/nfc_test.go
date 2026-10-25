package nfc

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// japanese is a sentence in NFC, written as code points.
const japanese = "5F53 793E 306E 4E8B 696D 5185 5BB9 3092 3054 7D39 4ECB 3057 307E 3059 3002"

func TestNormalizationTest(t *testing.T) {
	data, err := os.ReadFile("testdata/NormalizationTest.txt.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	text, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(text), "\n")
	if want := "# NormalizationTest-" + unicodeVersion + ".txt"; lines[0] != want {
		t.Fatalf("first line = %q, want %q", lines[0], want)
	}
	var part string
	inPart1 := map[rune]bool{}
	n := 0
	for i, line := range lines {
		if line == "" || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, "@Part") {
			part, _, _ = strings.Cut(line, " ")
			continue
		}
		fields, _, _ := strings.Cut(line, "#")
		f := strings.Split(fields, ";")
		if len(f) != 6 {
			t.Fatalf("line %d: want 5 columns: %q", i+1, line)
		}
		var c [5]string
		for j := range c {
			c[j] = fromHex(t, f[j])
		}
		n++
		if part == "@Part1" {
			r, _ := utf8.DecodeRuneInString(c[0])
			inPart1[r] = true
		}
		want := [5]string{c[1], c[1], c[1], c[3], c[3]}
		for j, s := range c {
			if got := Normalize(s); got != want[j] {
				t.Errorf("line %d: Normalize(c%d) = %+q, want %+q", i+1, j+1, got, want[j])
			}
			if got := IsNormalized(s); got != (s == want[j]) {
				t.Errorf("line %d: IsNormalized(c%d) = %t", i+1, j+1, got)
			}
		}
	}
	if n < 10000 {
		t.Fatalf("read only %d test lines", n)
	}
	for r := range rune(unicode.MaxRune + 1) {
		if inPart1[r] || !utf8.ValidRune(r) {
			continue
		}
		s := string(r)
		if got := Normalize(s); got != s || !IsNormalized(s) {
			t.Errorf("U+%04X: Normalize = %+q, IsNormalized = %t", r, got, IsNormalized(s))
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "ASCII", in: "0041 0062", want: "0041 0062"},
		{name: "composed", in: "00E9", want: "00E9"},
		{name: "compose", in: "0065 0301", want: "00E9"},
		{name: "kana voiced mark", in: "3053 3099", want: "3054"},
		{name: "singleton", in: "212B", want: "00C5"},
		{name: "ohm singleton", in: "2126", want: "03A9"},
		{name: "composition exclusion", in: "0958", want: "0915 093C"},
		{name: "non-starter decomposition", in: "0344", want: "0308 0301"},
		{name: "reorder", in: "0061 0301 0316", want: "00E1 0316"},
		{name: "blocked", in: "0061 0301 0301", want: "00E1 0301"},
		{name: "maybe but normalized", in: "0300", want: "0300"},
		{name: "Hangul LV", in: "1100 1161", want: "AC00"},
		{name: "Hangul LVT", in: "1100 1161 11A8", want: "AC01"},
		{name: "Hangul LV and T", in: "AC00 11A8", want: "AC01"},
		{name: "Hangul U+11A7 is not a T", in: "1100 1161 11A7", want: "AC00 11A7"},
		{name: "starters", in: "0B47 0B3E", want: "0B4B"},
		{name: "starters not adjacent", in: "0B47 0301 0B3E", want: "0B47 0301 0B3E"},
		{name: "Kirat Rai", in: "16D63 16D68", want: "16D6A"},
		{name: "Gurung Khema", in: "1611E 16121", want: "16121 1611E"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, want := fromHex(t, tt.in), fromHex(t, tt.want)
			if got := Normalize(in); got != want {
				t.Errorf("Normalize(%+q) = %+q, want %+q", in, got, want)
			}
			if got := IsNormalized(in); got != (in == want) {
				t.Errorf("IsNormalized(%+q) = %t, want %t", in, got, in == want)
			}
		})
	}
}

func TestNormalizeInvalidUTF8(t *testing.T) {
	acute, below := string(rune(0x0301)), string(rune(0x0316))
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "byte", in: "\xff", want: "\xff"},
		{name: "between base and mark", in: "e\xff" + acute, want: "e\xff" + acute},
		{name: "between marks", in: acute + "\xff" + below, want: acute + "\xff" + below},
		{name: "encoded surrogate", in: "\xed\xa0\x80" + acute, want: "\xed\xa0\x80" + acute},
		{name: "before base", in: "\xff" + "e" + acute, want: "\xff" + string(rune(0x00E9))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.in); got != tt.want {
				t.Errorf("Normalize(%+q) = %+q, want %+q", tt.in, got, tt.want)
			}
			if got := IsNormalized(tt.in); got != (tt.in == tt.want) {
				t.Errorf("IsNormalized(%+q) = %t, want %t", tt.in, got, tt.in == tt.want)
			}
		})
	}
}

func TestNormalizeLongRun(t *testing.T) {
	grave, acute := string(rune(0x0300)), string(rune(0x0301))
	graveBelow, acuteBelow := string(rune(0x0316)), string(rune(0x0317))
	in := strings.Repeat(grave+graveBelow+acute+acuteBelow, 25000)
	want := strings.Repeat(graveBelow+acuteBelow, 25000) + strings.Repeat(grave+acute, 25000)
	if Normalize(in) != want {
		t.Error("a long run of combining marks is not stably reordered")
	}
}

func TestNormalizeAllocs(t *testing.T) {
	for _, s := range []string{"hello, world", fromHex(t, japanese)} {
		if n := testing.AllocsPerRun(10, func() { Normalize(s); IsNormalized(s) }); n != 0 {
			t.Errorf("Normalize and IsNormalized of %+q allocate %v times", s, n)
		}
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{
		"",
		"abc",
		fromHex(f, "0065 0301 0316"),
		"\xff" + fromHex(f, "0301"),
		fromHex(f, "5F53 793E 3053 3099"),
		fromHex(f, "1100 1161 11A8"),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := Normalize(s)
		if full := normalize(s); full != n {
			t.Errorf("Normalize(%+q) = %+q, but normalize = %+q", s, n, full)
		}
		if again := Normalize(n); again != n {
			t.Errorf("Normalize(%+q) = %+q, but Normalize of that = %+q", s, n, again)
		}
		if !IsNormalized(n) {
			t.Errorf("IsNormalized(Normalize(%+q)) = false", s)
		}
		if got := IsNormalized(s); got != (n == s) {
			t.Errorf("IsNormalized(%+q) = %t, but Normalize returns %+q", s, got, n)
		}
		if utf8.ValidString(s) && !utf8.ValidString(n) {
			t.Errorf("Normalize(%+q) = %+q is not valid UTF-8", s, n)
		}
	})
}

func BenchmarkNormalize(b *testing.B) {
	grave, circumflex, acute := string(rune(0x0300)), string(rune(0x0302)), string(rune(0x0301))
	tests := []struct {
		name string
		s    string
	}{
		{name: "ASCII", s: strings.Repeat("hello, world ", 100)},
		{name: "Japanese", s: strings.Repeat(fromHex(b, japanese), 30)},
		{name: "decomposed", s: strings.Repeat("Cre"+grave+"me bru"+circumflex+"le"+acute+"e et cafe"+acute+" au lait. ", 30)},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			b.SetBytes(int64(len(tt.s)))
			for b.Loop() {
				Normalize(tt.s)
			}
		})
	}
}
