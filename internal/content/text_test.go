package content

import (
	"strings"
	"testing"
)

// assertErrorMsg checks that err contains msg, or that err is nil if msg is "".
func assertErrorMsg(t *testing.T, err error, msg string) {
	t.Helper()
	if msg == "" {
		if err != nil {
			t.Errorf("error = %q, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Errorf("error = %v, want %q", err, msg)
	}
}

// "e" + acute is the NFD form of eAcute.
const (
	acute  = string(rune(0x0301))
	eAcute = string(rune(0x00E9))
)

func TestNormalizeText(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		multiline bool
		want      string
	}{
		{name: "NUL", in: "a\x00b", want: "ab"},
		{name: "DEL", in: "a\x7fb", want: "ab"},
		{name: "C1 control", in: "a\u0080b", want: "ab"},
		{name: "tab", in: "a\tb", want: "a\tb"},
		{name: "newline", in: "a\nb", want: "a\nb"},
		{name: "NEL", in: "a\u0085b", want: "a\u0085b"},
		{name: "line separator", in: "a\u2028b", want: "a\u2028b"},
		{name: "no-break space", in: "a\u00a0b", want: "a\u00a0b"},
		{name: "empty", in: "", want: ""},
		{name: "NFD", in: "cafe" + acute, want: "caf" + eAcute},
		{name: "composed after removing NUL", in: "e\x00" + acute, want: eAcute},
		{name: "invalid UTF-8", in: "a\xffb", want: "a\ufffdb"},
		{name: "multiline CRLF", in: "a\r\nb", multiline: true, want: "a\nb"},
		{name: "multiline CR", in: "a\rb", multiline: true, want: "a\nb"},
		{name: "multiline CR and CRLF", in: "a\r\r\nb", multiline: true, want: "a\n\nb"},
		{name: "multiline tab and NUL", in: "a\tb\x00", multiline: true, want: "a\tb"},
		{name: "multiline VT", in: "a\vb", multiline: true, want: "ab"},
		{name: "multiline NEL", in: "a\u0085b", multiline: true, want: "ab"},
		{name: "multiline NFD", in: "e" + acute + "\r\n", multiline: true, want: eAcute + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeText(tt.in, tt.multiline)
			if got != tt.want {
				t.Errorf("normalizeText(%q, %t) = %q, want %q", tt.in, tt.multiline, got, tt.want)
			}
			if again := normalizeText(got, tt.multiline); again != got {
				t.Errorf("normalizeText(%q, %t) = %q, want it unchanged", got, tt.multiline, again)
			}
		})
	}
}

func TestCleanText(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		multiline bool
		want      string
	}{
		{name: "NUL", in: "a\x00b", want: "ab"},
		{name: "CRLF", in: "a\r\nb", want: "a\r\nb"},
		{name: "NFD", in: "cafe" + acute, want: "cafe" + acute},
		{name: "invalid UTF-8", in: "a\xffb", want: "a" + string(rune(0xFFFD)) + "b"},
		{name: "multiline CRLF", in: "a\r\nb", multiline: true, want: "a\nb"},
		{name: "multiline CR", in: "a\rb", multiline: true, want: "a\nb"},
		{name: "multiline tab and NUL", in: "a\tb\x00", multiline: true, want: "a\tb"},
		{name: "multiline NFD", in: "e" + acute + "\r\n", multiline: true, want: "e" + acute + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanText(tt.in, tt.multiline)
			if got != tt.want {
				t.Errorf("cleanText(%q, %t) = %q, want %q", tt.in, tt.multiline, got, tt.want)
			}
			if again := cleanText(got, tt.multiline); again != got {
				t.Errorf("cleanText(%q, %t) = %q, want it unchanged", got, tt.multiline, again)
			}
		})
	}
}

func TestCheckText(t *testing.T) {
	const (
		lineBreak = "contains a line break or tab; use hardBreak for line breaks"
		control   = "contains control characters; normalize it first"
	)
	tests := []struct {
		name      string
		in        string
		multiline bool
		msg       string
	}{
		{name: "Japanese", in: "当社の事業内容"},
		{name: "space", in: "a b"},
		{name: "no-break space", in: "a\u00a0b"},
		{name: "ideographic space", in: "a\u3000b"},
		{name: "empty", in: ""},
		{name: "NFC", in: "caf" + eAcute},
		{name: "NFD", in: "cafe" + acute, msg: "not in Unicode NFC; normalize it first"},
		{name: "newline", in: "a\nb", msg: lineBreak},
		{name: "CR", in: "a\rb", msg: lineBreak},
		{name: "tab", in: "a\tb", msg: lineBreak},
		{name: "VT", in: "a\vb", msg: lineBreak},
		{name: "FF", in: "a\fb", msg: lineBreak},
		{name: "NEL", in: "a\u0085b", msg: lineBreak},
		{name: "line separator", in: "a\u2028b", msg: lineBreak},
		{name: "paragraph separator", in: "a\u2029b", msg: lineBreak},
		{name: "NUL", in: "a\x00b", msg: control},
		{name: "DEL", in: "a\x7fb", msg: control},
		{name: "C1 control", in: "a\u0080b", msg: control},
		{name: "line break before other controls", in: "a\x00\nb", msg: lineBreak},
		{name: "invalid UTF-8", in: "a\xffb", msg: "invalid UTF-8"},
		{name: "multiline newline and tab", in: "a\n\tb", multiline: true},
		{name: "multiline line separator", in: "a\u2028b", multiline: true},
		{name: "multiline CR", in: "a\rb", multiline: true, msg: control},
		{name: "multiline NUL", in: "a\x00b", multiline: true, msg: control},
		{name: "multiline VT", in: "a\vb", multiline: true, msg: control},
		{name: "multiline invalid UTF-8", in: "a\xffb", multiline: true, msg: "invalid UTF-8"},
		{name: "multiline NFD", in: "e" + acute + "\n", multiline: true, msg: "not in Unicode NFC; normalize it first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkText(tt.in, tt.multiline), tt.msg)
		})
	}
}

func TestCheckLine(t *testing.T) {
	const lineBreak = "contains a line break or tab"
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "space", in: "a b"},
		{name: "Japanese", in: "会社外観"},
		{name: "newline", in: "a\nb", msg: lineBreak},
		{name: "tab", in: "a\tb", msg: lineBreak},
		{name: "line separator", in: "a" + string(rune(0x2028)) + "b", msg: lineBreak},
		{name: "NUL", in: "a\x00b", msg: "contains control characters; normalize it first"},
		{name: "NFD", in: "cafe" + acute, msg: "not in Unicode NFC; normalize it first"},
		{name: "invalid UTF-8", in: "a\xffb", msg: "invalid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkLine(tt.in)
			assertErrorMsg(t, err, tt.msg)
			if err != nil && strings.Contains(err.Error(), "hardBreak") {
				t.Errorf("error = %q, want no mention of hardBreak", err)
			}
		})
	}
}

func TestCheckRaw(t *testing.T) {
	const control = "contains control characters; normalize it first"
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "HTML", in: `<p class="note">a</p>`},
		{name: "empty", in: ""},
		{name: "newline and tab", in: "<ul>\n\t<li>a</li>\n</ul>"},
		{name: "NFD", in: `<img src="/cafe` + acute + `.jpg">`},
		{name: "CR", in: "<p>a</p>\r\n", msg: control},
		{name: "NUL", in: "a\x00b", msg: control},
		{name: "invalid UTF-8", in: "a\xffb", msg: "invalid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkRaw(tt.in), tt.msg)
		})
	}
}

func TestCheckName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "word", in: "primary"},
		{name: "hyphen and underscore", in: "a-b_c"},
		{name: "upper case and digit", in: "A1"},
		{name: "ID", in: "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3"},
		{name: "empty", in: "", msg: "empty name"},
		{name: "space", in: "a b", msg: `invalid name "a b": use ASCII letters, digits, "-" and "_"`},
		{name: "dot", in: "a.b", msg: "invalid name"},
		{name: "slash", in: "a/b", msg: "invalid name"},
		{name: "Japanese", in: "見出し", msg: "invalid name"},
		{name: "non-ASCII letter", in: "caf\u00e9", msg: "invalid name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkName(tt.in), tt.msg)
		})
	}
}

func TestCheckBlockID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "letters and digits", in: "b_9x3v7k5m"},
		{name: "digits", in: "b_00000000"},
		{name: "empty", in: "", msg: "invalid block ID"},
		{
			name: "upper case",
			in:   "b_9X3V7K5M",
			msg:  `invalid block ID "b_9X3V7K5M": use "b_" and 8 lowercase ASCII letters or digits`,
		},
		{name: "7 characters", in: "b_9x3v7k5", msg: "invalid block ID"},
		{name: "9 characters", in: "b_9x3v7k5mm", msg: "invalid block ID"},
		{name: "other prefix", in: "c_9x3v7k5m", msg: "invalid block ID"},
		{name: "hyphen", in: "b-9x3v7k5m", msg: "invalid block ID"},
		{name: "underscore", in: "b_9x3v7k5_", msg: "invalid block ID"},
		{name: "non-ASCII letter in 10 bytes", in: "b_9x3v7k" + eAcute, msg: "invalid block ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkBlockID(tt.in), tt.msg)
		})
	}
}

func TestNormalizeHref(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "surrounding spaces", in: " https://example.com ", want: "https://example.com"},
		{name: "surrounding tab and newline", in: "\thttps://example.com\n", want: "https://example.com"},
		{name: "tab in scheme", in: "java\tscript:alert(1)", want: "javascript:alert(1)"},
		{name: "NUL in host", in: "https://exa\x00mple.com", want: "https://example.com"},
		{name: "surrounding ideographic spaces", in: "\u3000/about/\u3000", want: "/about/"},
		{name: "space in path", in: "https://x/a b", want: "https://x/a b"},
		{name: "invalid UTF-8", in: "https://example.com/\xff", want: "https://example.com/\ufffd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeHref(tt.in)
			if got != tt.want {
				t.Errorf("normalizeHref(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if again := normalizeHref(got); again != got {
				t.Errorf("normalizeHref(%q) = %q, want it unchanged", got, again)
			}
		})
	}
}

func TestCheckHref(t *testing.T) {
	const (
		normalize   = "contains control characters or surrounding spaces; normalize it first"
		backslash   = "backslashes are not allowed"
		absolute    = `must be an absolute URL or a path starting with "/"`
		credentials = "credentials in URLs are not allowed"
	)
	tests := []struct {
		name string
		in   string
		msg  string
	}{
		{name: "https", in: "https://example.com"},
		{name: "http with query and fragment", in: "http://example.com/a?b=1#c"},
		{name: "upper case", in: "HTTPS://EXAMPLE.COM/"},
		{name: "IDN", in: "https://例え.jp/パス"},
		{name: "space in path", in: "https://x/a b"},
		{name: "IPv6 and port", in: "https://[::1]:8080/"},
		{name: "mailto", in: "mailto:info@example.com"},
		{name: "mailto with query", in: "mailto:info@example.com?subject=hi"},
		{name: "tel", in: "tel:+81-3-1234-5678"},
		{name: "root", in: "/"},
		{name: "path", in: "/about/"},
		{name: "query", in: "/?p=123"},

		{name: "empty", in: "", msg: "empty URL"},
		{name: "invalid UTF-8", in: "https://example.com/\xff", msg: "invalid UTF-8"},
		{name: "leading space", in: " https://example.com", msg: normalize},
		{name: "trailing newline", in: "https://example.com\n", msg: normalize},
		{name: "tab in scheme", in: "java\tscript:alert(1)", msg: normalize},
		{name: "backslash after slash", in: `/\evil.example`, msg: backslash},
		{name: "backslashes after scheme", in: `https:\\evil.example`, msg: backslash},
		{name: "space in host", in: "https://exa mple.com", msg: "invalid URL"},
		{name: "invalid escape", in: "/%zz", msg: "invalid URL"},
		{name: "empty scheme", in: ":foo", msg: "invalid URL"},
		{name: "relative path", in: "about", msg: absolute},
		{name: "fragment", in: "#top", msg: absolute},
		{name: "query without path", in: "?q=1", msg: absolute},
		{name: "host without scheme", in: "example.com", msg: absolute},
		{name: "protocol-relative", in: "//evil.example", msg: "protocol-relative URLs are not allowed"},
		{name: "javascript", in: "javascript:alert(1)", msg: `scheme "javascript" is not allowed`},
		{name: "javascript in mixed case", in: "JavaScript:alert(1)", msg: `scheme "javascript" is not allowed`},
		{name: "data", in: "data:text/html,x", msg: `scheme "data" is not allowed`},
		{name: "vbscript", in: "vbscript:x", msg: `scheme "vbscript" is not allowed`},
		{name: "file", in: "file:///etc/passwd", msg: `scheme "file" is not allowed`},
		{name: "ftp", in: "ftp://example.com", msg: `scheme "ftp" is not allowed`},
		{name: "host and port without scheme", in: "localhost:3000", msg: `scheme "localhost" is not allowed`},
		{name: "https without slashes", in: "https:example.com", msg: "missing host"},
		{name: "https without host", in: "https://", msg: "missing host"},
		{name: "http with path only", in: "http:/path", msg: "missing host"},
		{name: "user and password", in: "https://user:pass@example.com", msg: credentials},
		{name: "user", in: "https://user@example.com", msg: credentials},
		{name: "empty user", in: "https://@example.com", msg: credentials},
		{name: "mailto without address", in: "mailto:", msg: "missing address"},
		{name: "mailto with authority", in: "mailto://x", msg: "missing address"},
		{name: "tel without number", in: "tel:", msg: "missing address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, checkHref(tt.in), tt.msg)
		})
	}
}

func TestCheckSize(t *testing.T) {
	const msg = "too long: 1048577 bytes (max 1048576)"
	long := strings.Repeat("a", MaxTextBytes)
	tests := []struct {
		name    string
		check   func(string) error
		longest string // the longest string that check accepts
	}{
		{name: "checkSize", check: checkSize, longest: long},
		{name: "checkText", check: func(s string) error { return checkText(s, false) }, longest: long},
		{name: "checkText multiline", check: func(s string) error { return checkText(s, true) }, longest: long},
		{name: "checkLine", check: checkLine, longest: long},
		{name: "checkRaw", check: checkRaw, longest: long},
		{name: "checkName", check: checkName, longest: long},
		{name: "checkHref", check: checkHref, longest: "/" + long[1:]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErrorMsg(t, tt.check(tt.longest), "")
			assertErrorMsg(t, tt.check(tt.longest+"a"), msg)
			// The size is checked first, before UTF-8.
			assertErrorMsg(t, tt.check(tt.longest+"\xff"), msg)
		})
	}
}
