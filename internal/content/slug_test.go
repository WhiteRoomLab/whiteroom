package content

import (
	"strings"
	"testing"
	"unicode"
)

func TestCheckSlug(t *testing.T) {
	const (
		upper   = "contains upper case letters; normalize it first"
		control = "contains control characters; normalize it first"
		ascii   = `is not allowed in slugs: use lower case letters, digits, "-" and "_" in ASCII`
		notNFC  = "not in Unicode NFC; normalize it first"
		edge    = `slugs cannot start or end with "-"`
	)
	tests := []struct {
		name string
		in   string
		msg  string // part of the error, or "" for none
	}{
		{name: "ASCII", in: "about-us_2024"},
		{name: "digits", in: "123"},
		{name: "underscore at the edges", in: "_a_"},
		{name: "Japanese", in: "会社概要"},
		{name: "katakana with a middle dot", in: "サービス・料金"},
		{name: "full-width parentheses", in: "会社概要（2024年）"},
		{name: "ideographic space", in: "代表挨拶\u3000山田太郎"},
		{name: "prolonged sound mark", in: "データー"},
		{name: "emoji with a variation selector", in: "\u2764\ufe0f"},
		{name: "accented letter", in: "caf\u00e9"},
		{name: "Cyrillic", in: "привет"},
		{name: "Arabic", in: "مرحبا"},
		{name: "symbol", in: "\u2605"},
		{name: "full-width digits", in: "\uff12\uff10\uff12\uff14"},
		{name: "decomposed dakuten", in: "\u30ab\u3099", msg: notNFC},
		{name: "decomposed accent", in: "cafe\u0301", msg: notNFC},
		{name: "upper case ASCII", in: "About", msg: upper},
		{name: "upper case letter", in: "\u00c9t\u00e9", msg: upper},
		{name: "upper case Cyrillic", in: "Привет", msg: upper},
		{name: "dotted capital I", in: "\u0130", msg: upper},
		{name: "circled capital A", in: "\u24b6", msg: upper},
		{name: "NUL", in: "a\x00b", msg: control},
		{name: "C1 control", in: "a\u0080b", msg: control},
		{name: "space", in: "a b", msg: `character ' ' ` + ascii},
		{name: "tab", in: "a\tb", msg: `character '\t' ` + ascii},
		{name: "newline", in: "a\nb", msg: `character '\n' ` + ascii},
		{name: "dot", in: "a.html", msg: `character '.' ` + ascii},
		{name: "dots", in: "..", msg: `character '.' ` + ascii},
		{name: "slash", in: "a/b", msg: `character '/' ` + ascii},
		{name: "backslash", in: `a\b`, msg: `character '\\' ` + ascii},
		{name: "percent", in: "%e4%bc%9a", msg: `character '%' ` + ascii},
		{name: "question mark", in: "a?b", msg: `character '?' ` + ascii},
		{name: "hash", in: "a#b", msg: `character '#' ` + ascii},
		{name: "tilde", in: "a~b", msg: `character '~' ` + ascii},
		{name: "plus", in: "a+b", msg: `character '+' ` + ascii},
		{name: "NEL", in: "a\u0085b", msg: `(U+0085) is not allowed in slugs`},
		{name: "no-break space", in: "a\u00a0b", msg: `(U+00A0) is not allowed in slugs`},
		{name: "en quad", in: "a\u2000b", msg: `(U+2000) is not allowed in slugs`},
		{name: "line separator", in: "a\u2028b", msg: `(U+2028) is not allowed in slugs`},
		{name: "zero width space", in: "a\u200bb", msg: `(U+200B) is not allowed in slugs`},
		{name: "zero width joiner", in: "a\u200db", msg: `(U+200D) is not allowed in slugs`},
		{name: "right-to-left override", in: "a\u202eb", msg: `(U+202E) is not allowed in slugs`},
		{name: "byte order mark", in: "\ufeffa", msg: `(U+FEFF) is not allowed in slugs`},
		{name: "soft hyphen", in: "a\u00adb", msg: `(U+00AD) is not allowed in slugs`},
		{name: "private use", in: "a\ue000b", msg: `(U+E000) is not allowed in slugs`},
		{name: "unassigned", in: "a\u0378b", msg: `(U+0378) is not allowed in slugs`},
		{name: "noncharacter", in: "a\ufdd0b", msg: `(U+FDD0) is not allowed in slugs`},
		{name: "leading hyphen", in: "-about", msg: edge},
		{name: "trailing hyphen", in: "about-", msg: edge},
		{name: "hyphen", in: "-", msg: edge},
		{name: "longest", in: strings.Repeat("a", MaxSlugBytes)},
		{name: "longest Japanese", in: strings.Repeat("あ", MaxSlugBytes/3)},
		{name: "too long", in: strings.Repeat("a", MaxSlugBytes+1), msg: "too long: 201 bytes (max 200)"},
		{name: "too long Japanese", in: strings.Repeat("あ", MaxSlugBytes/3+1), msg: "too long: 201 bytes (max 200)"},
		{name: "invalid UTF-8", in: "a\xffb", msg: "invalid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkSlug(tt.in), tt.msg)
		})
	}
}

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ASCII", in: "About-Us", want: "about-us"},
		{name: "already normal", in: "会社概要", want: "会社概要"},
		{name: "accented capital", in: "\u00c9T\u00c9", want: "\u00e9t\u00e9"},
		{name: "decomposed", in: "E\u0301", want: "\u00e9"},
		{name: "decomposed dakuten", in: "\u30ab\u3099", want: "\u30ac"},
		{name: "dotted capital I", in: "\u0130", want: "i"},
		{name: "Kelvin sign", in: "\u212a", want: "k"},
		{name: "Ohm sign", in: "\u2126", want: "\u03c9"},
		{name: "control", in: "a\x00b\u0080c", want: "abc"},
		{name: "spaces are kept", in: " a b ", want: " a b "},
		{name: "tab is kept", in: "a\tb", want: "a\tb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeSlug(tt.in); got != tt.want {
				t.Errorf("normalizeSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeSlugEveryRune checks that normalizeSlug leaves nothing that checkSlug asks to normalize.
func TestNormalizeSlugEveryRune(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		// Unassigned and private use characters, which normalizeSlug leaves as they are, take most of the time.
		if !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.Cc, unicode.Cf) {
			continue
		}
		in := "a" + string(r) + "a"
		if err := checkSlug(normalizeSlug(in)); err != nil && strings.Contains(err.Error(), "normalize it first") {
			t.Errorf("checkSlug(normalizeSlug(%q)) = %v", in, err)
		}
	}
}
