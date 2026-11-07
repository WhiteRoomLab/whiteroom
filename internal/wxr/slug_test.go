package wxr

import "testing"

func TestDecodeSlug(t *testing.T) {
	replacement := string(rune(0xfffd))
	nfd := string([]rune{0x304b, 0x3099}) // か and a combining mark
	tests := []struct {
		name, in, want, err string
	}{
		{name: "ASCII", in: "hello-world_1.2", want: "hello-world_1.2"},
		{name: "empty", in: "", want: ""},
		{name: "Japanese", in: "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", want: "お知らせ"},
		{name: "upper case digits", in: "%E3%81%8A%E7%9F%A5%E3%82%89%E3%81%9B", want: "お知らせ"},
		{name: "mixed case digits", in: "%e3%81%8A%e7%9F%a5%E3%82%89%e3%81%9B", want: "お知らせ"},
		{name: "encoded and not", in: "news-%e3%81%8a-2026", want: "news-お-2026"},
		{name: "not encoded Japanese", in: "お知らせ", want: "お知らせ"},
		{name: "partly encoded Japanese", in: "お%e7%9f%a5らせ", want: "お知らせ"},
		{name: "plus and spaces are not changed", in: "a+b c", want: "a+b c"},
		{name: "encoded ASCII", in: "%2d%41%7e%2F", want: "-A~/"},
		{name: "encoded percent", in: "100%25", want: "100%"},
		{name: "decoded once", in: "%2541", want: "%41"},
		{name: "case is kept", in: "ABC-%e3%81%82-DEF", want: "ABC-あ-DEF"},
		{name: "not normalized", in: "%e3%81%8b%e3%82%99", want: nfd},
		{name: "U+FFFD", in: "%ef%bf%bd", want: replacement},
		{name: "percent at the end", in: "abc%", err: "invalid percent-encoding at byte 3"},
		{name: "one digit at the end", in: "abc%e", err: "invalid percent-encoding at byte 3"},
		{name: "not digits", in: "%zz", err: "invalid percent-encoding at byte 0"},
		{name: "a digit and another character", in: "a%eg", err: "invalid percent-encoding at byte 1"},
		{name: "after decoded bytes", in: "%e3%81%8a%", err: "invalid percent-encoding at byte 9"},
		{name: "cut UTF-8", in: "%e3%81", err: "not valid UTF-8 after decoding"},
		{name: "a byte that is not UTF-8", in: "%ff", err: "not valid UTF-8 after decoding"},
		{name: "an overlong character", in: "%c0%80", err: "not valid UTF-8 after decoding"},
		{name: "a surrogate", in: "%ed%a0%80", err: "not valid UTF-8 after decoding"},
		{name: "NUL", in: "%00", err: "contains a control character after decoding"},
		{name: "line feed", in: "a%0ab", err: "contains a control character after decoding"},
		{name: "DEL", in: "%7f", err: "contains a control character after decoding"},
		{name: "a C1 control character", in: "%c2%85", err: "contains a control character after decoding"},
		{name: "a control character that is not encoded", in: "a\tb", err: "contains a control character after decoding"},
		{name: "UTF-8 that is not valid, not encoded", in: "a\xffb", err: "not valid UTF-8 after decoding"},
		{name: "UTF-8 and a control character that is not valid", in: "%ff%00", err: "not valid UTF-8 after decoding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeSlug(tt.in)
			switch {
			case tt.err != "":
				if err == nil || err.Error() != tt.err || got != "" {
					t.Errorf("DecodeSlug(%q) = %q, %v, want the error %q", tt.in, got, err, tt.err)
				}
			case err != nil || got != tt.want:
				t.Errorf("DecodeSlug(%q) = %q, %v, want %q", tt.in, got, err, tt.want)
			}
		})
	}
}
