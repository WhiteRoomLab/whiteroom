package content

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Messages of checkCSSValue.
const (
	cssNotRule    = "a preset is a value, not a declaration or a rule"
	cssBackslash  = "backslashes are not allowed; write the characters themselves, not CSS escapes"
	cssURL        = "() is not allowed; write URLs in the CSS files of assets/ instead"
	cssCommentCut = `; a "#" after a space starts a comment in YAML, so quote the whole value`
	cssSpace      = "starts or ends with a space"
)

var checkCSSValueTests = []struct {
	name string
	in   string
	msg  string // "" if valid
}{
	{name: "hex color", in: "#1e73be"},
	{name: "rgb with alpha", in: "rgb(0 0 0 / 50%)"},
	{name: "oklch", in: "oklch(25% 0 0)"},
	{name: "clamp", in: "clamp(1.25rem, 1rem + 1vw, 1.5rem)"},
	{name: "nested parentheses", in: "calc((1rem + 2px) * 2)"},
	{name: "var", in: "var(--x)"},
	{name: "double-quoted font family", in: `"Noto Sans JP", sans-serif`},
	{name: "single-quoted font family", in: `'Hiragino Sans', sans-serif`},
	{name: "Japanese font family", in: "游ゴシック体, YuGothic, sans-serif"},
	{name: "quoted Japanese font family", in: `"ヒラギノ角ゴ ProN", sans-serif`},
	{name: "gradient", in: "linear-gradient(135deg, #f093fb 0%, #f5576c 100%)"},
	{name: "shadow", in: "0 2px 4px rgba(0, 0, 0, 0.1)"},
	{name: "length", in: "0.875rem"},
	{name: "every character outside quotes", in: "aZ09 #%(),./+-*_"},
	{name: "function named myurl", in: "myurl(x)"},
	{name: "function named -url", in: "-url(x)"},
	{name: "function named url2", in: "url2(x)"},
	{name: "function named src_", in: "src_(x)"},
	{name: "function named あurl", in: "あurl(x)"},
	// CSS matches the names of functions ASCII case-insensitively.
	{name: "function with a long s", in: string(rune(0x017F)) + "rc(x)"},
	{name: "function with fullwidth letters", in: string([]rune{0xFF55, 0xFF52, 0xFF4C}) + "(x)"},
	// An identifier and a parenthesized block, not a function.
	{name: "url before a space", in: "url (x)"},
	{name: "url before a string", in: `url"x"(y)`},
	{name: "url without parentheses", in: "url"},
	{name: "symbols in quotes", in: `"a;b{c}d[e]f!g@h:i=j&k?l^m|n~o$p` + "`" + `q"`},
	{name: "comment in quotes", in: `"/* x */"`},
	{name: "url in quotes", in: `"url(x)"`},
	{name: "parentheses in quotes", in: `")" '('`},
	{name: "quotes in quotes", in: `"it's" '"'`},
	{name: "empty strings", in: `"" ''`},
	{name: "slash and asterisk around a string", in: `1/"x"*2`},
	{name: "asterisk before a slash", in: "calc(2 */ 1)"},
	{name: "non-ASCII space inside", in: "a" + string(rune(0x3000)) + "b"},

	{name: "empty", in: "", msg: "empty value"},
	{name: "leading space", in: " 1rem", msg: cssSpace},
	{name: "trailing space", in: "1rem ", msg: cssSpace},
	{name: "only a space", in: " ", msg: cssSpace},
	{name: "trailing ideographic space", in: "1rem" + string(rune(0x3000)), msg: cssSpace},
	{name: "leading no-break space", in: string(rune(0x00A0)) + "1rem", msg: cssSpace},
	{name: "line break", in: "1rem\n2rem", msg: "contains a line break or tab"},
	{name: "line break in quotes", in: "\"a\nb\"", msg: "contains a line break or tab"},
	{name: "tab", in: "0\t2px", msg: "contains a line break or tab"},
	{name: "form feed", in: "a\fb", msg: "contains a line break or tab"},
	{name: "control character", in: "a" + string(rune(0x0001)) + "b", msg: "contains control characters; normalize it first"},
	{name: "DEL", in: "a\x7fb", msg: "contains control characters; normalize it first"},
	{name: "NFD", in: `"Cafe` + acute + `", serif`, msg: "not in Unicode NFC; normalize it first"},
	{name: "invalid UTF-8", in: "a\xffb", msg: "invalid UTF-8"},
	{name: "too long", in: strings.Repeat("a", MaxTextBytes+1), msg: "too long: 1048577 bytes (max 1048576)"},

	{name: "declaration", in: "red; color: blue", msg: `";" is not allowed; ` + cssNotRule},
	{name: "end of the rule", in: "red} body {color: blue", msg: `"}" is not allowed; ` + cssNotRule},
	{name: "block", in: "{a}", msg: `"{" is not allowed; ` + cssNotRule},
	{name: "important", in: "red !important", msg: `"!" is not allowed; a preset cannot be !important`},
	{name: "escape", in: `\31 rem`, msg: cssBackslash},
	{name: "escape in quotes", in: `"a\"b"`, msg: cssBackslash},
	{name: "less-than sign", in: "a<b", msg: `"<" is not allowed`},
	{name: "end of the style element in quotes", in: `"</style><script>alert(1)</script>"`, msg: `"<" is not allowed`},
	{name: "greater-than sign in quotes", in: `"a>b"`, msg: `">" is not allowed`},
	{name: "CDC", in: "a -->", msg: `">" is not allowed`},
	{name: "at-rule", in: "@import x", msg: `"@" is not allowed outside quotes`},
	{name: "colon", in: "a:b", msg: `":" is not allowed outside quotes`},
	{name: "brackets", in: "[a]", msg: `"[" is not allowed outside quotes`},
	{name: "backquote", in: "`a`", msg: "\"`\" is not allowed outside quotes"},
	{name: "first problem", in: "a:b;c", msg: `":" is not allowed outside quotes`},

	{name: "comment", in: "1rem /* x */", msg: "comments are not allowed"},
	{name: "comment after a string", in: `"a"/*b*/`, msg: "comments are not allowed"},
	{name: "unexpected parenthesis", in: "a)", msg: `unexpected ")"`},
	{name: "parenthesis after a function", in: "rgb(0 0 0))", msg: `unexpected ")"`},
	{name: "parentheses in the wrong order", in: ")(", msg: `unexpected ")"`},
	{name: "unclosed parenthesis", in: "rgb(0 0 0", msg: `unclosed "("` + cssCommentCut},
	{name: "unclosed nested parenthesis", in: "calc((1rem + 2px) * 2", msg: `unclosed "("` + cssCommentCut},
	{name: "gradient cut by a YAML comment", in: "linear-gradient(135deg,", msg: `unclosed "("` + cssCommentCut},
	{name: "unclosed string", in: `"Noto Sans JP, sans-serif`, msg: "unclosed string" + cssCommentCut},
	{name: "unclosed single-quoted string", in: `'a`, msg: "unclosed string" + cssCommentCut},
	{name: "mismatched quotes", in: `"a'`, msg: "unclosed string" + cssCommentCut},
	{name: "string after a string", in: `"a" "b`, msg: "unclosed string" + cssCommentCut},

	{name: "url", in: "url(x)", msg: "url" + cssURL},
	{name: "url with a quoted URL", in: `url("/a.png")`, msg: "url" + cssURL},
	{name: "url in upper case", in: "URL(x)", msg: "URL" + cssURL},
	{name: "src", in: `src("a.woff2")`, msg: "src" + cssURL},
	{name: "src in mixed case", in: "sRc(x)", msg: "sRc" + cssURL},
	{name: "url after a string", in: `"a"url(x)`, msg: "url" + cssURL},
	{name: "url after a symbol", in: "+url(x)", msg: "url" + cssURL},
	{name: "url after a dot", in: ".url(x)", msg: "url" + cssURL},
	{name: "url in a function", in: "cross-fade(url(a.png), red)", msg: "url" + cssURL},
	// These also load the URLs in strings.
	{name: "image-set", in: `image-set("a.png" 1x)`, msg: "image-set" + cssURL},
	{name: "image-set in mixed case", in: "Image-Set(x)", msg: "Image-Set" + cssURL},
	{name: "image-set with the webkit prefix", in: `-webkit-image-set("a.png" 1x)`, msg: "-webkit-image-set" + cssURL},
	{name: "image", in: `image("a.png")`, msg: "image" + cssURL},
	{name: "image in a function", in: `cross-fade(image("a.png"), red)`, msg: "image" + cssURL},
	{name: "function named my-image", in: "my-image(x)"},
	{name: "function named imageset", in: "imageset(x)"},
	{name: "function named image-set2", in: "image-set2(x)"},
	{name: "function named moz-image-set", in: "-moz-image-set(x)"},
	{name: "function named webkit-image", in: "-webkit-image(x)"},
	{name: "function named x-webkit-image-set", in: "x-webkit-image-set(x)"},
}

func TestCheckCSSValue(t *testing.T) {
	for _, tt := range checkCSSValueTests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPresetValue(tt.in)
			if got := errorMsg(err); got != tt.msg {
				t.Errorf("CheckPresetValue(%q) = %q, want %q", tt.in, got, tt.msg)
			}
			if ok := cssValueOK(tt.in); ok != (tt.msg == "") {
				t.Errorf("cssValueOK(%q) = %t", tt.in, ok)
			}
		})
	}
}

// errorMsg returns the message of err, or "" for nil.
func errorMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestCheckCSSValueCharacters checks every printable ASCII character between two letters, and in quotes.
func TestCheckCSSValueCharacters(t *testing.T) {
	for c := byte(' '); c <= '~'; c++ {
		var outside, inside string // the messages, "" if valid
		switch c {
		case '"', '\'':
			outside = "unclosed string" + cssCommentCut
		case '(':
			outside = `unclosed "("` + cssCommentCut
		case ')':
			outside = `unexpected ")"`
		case ';', '{', '}':
			outside = fmt.Sprintf("%q is not allowed; %s", string(c), cssNotRule)
		case '!':
			outside = `"!" is not allowed; a preset cannot be !important`
		case '\\':
			outside, inside = cssBackslash, cssBackslash
		case '<', '>':
			outside = fmt.Sprintf("%q is not allowed", string(c))
			inside = outside
		case '[', ']', '@', ':', '=', '&', '?', '^', '|', '~', '$', '`':
			outside = fmt.Sprintf("%q is not allowed outside quotes", string(c))
		}
		in := "a" + string(c) + "b"
		if got := errorMsg(checkCSSValue(in)); got != outside {
			t.Errorf("checkCSSValue(%q) = %q, want %q", in, got, outside)
		}
		quoted := `"` + string(c) + `"`
		if c == '"' {
			quoted = `'"'`
		}
		if got := errorMsg(checkCSSValue(quoted)); got != inside {
			t.Errorf("checkCSSValue(%q) = %q, want %q", quoted, got, inside)
		}
	}
}

var (
	cssStrings   = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	cssFunctions = regexp.MustCompile(`(?:^|[^-_0-9A-Za-z\x{80}-\x{10FFFF}])` +
		`(?:[uU][rR][lL]|[sS][rR][cC]|[iI][mM][aA][gG][eE](?:-[sS][eE][tT])?|-[wW][eE][bB][kK][iI][tT]-[iI][mM][aA][gG][eE]-[sS][eE][tT])\(`)
)

// cssValueOK is a simple reference for checkCSSValue: it replaces the strings with spaces, and then checks the rest.
func cssValueOK(s string) bool {
	if checkLine(s) != nil || s == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return false
	}
	for _, str := range cssStrings.FindAllString(s, -1) {
		if strings.ContainsAny(str, `\<>`) {
			return false
		}
	}
	rest := cssStrings.ReplaceAllString(s, " ")
	const allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 #%(),./+-*_"
	depth := 0
	for _, r := range rest {
		if r < utf8.RuneSelf && !strings.ContainsRune(allowed, r) {
			return false
		}
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !strings.Contains(rest, "/*") && !cssFunctions.MatchString(rest)
}

// FuzzCheckCSSValue checks that checkCSSValue accepts exactly the values that cssValueOK accepts.
func FuzzCheckCSSValue(f *testing.F) {
	for _, tt := range checkCSSValueTests {
		if len(tt.in) <= 1000 {
			f.Add(tt.in)
		}
	}
	f.Fuzz(func(t *testing.T, s string) {
		err := checkCSSValue(s)
		if ok := cssValueOK(s); ok != (err == nil) {
			t.Errorf("checkCSSValue(%q) = %v, but cssValueOK = %t", s, err, ok)
		}
	})
}
