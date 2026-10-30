package content

import (
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// jaTranslationYAML is a translation file of a theme, with placeholders.
const jaTranslationYAML = `# 既定の言語の訳
readMore: 続きを読む
previous: 前へ
next: 次へ
pageOf: "{page} / {total} ページ"
notFound: ページが見つかりません
postedBy: "{name} が投稿"
`

// jaTranslation returns the translation of jaTranslationYAML.
func jaTranslation() ThemeTranslation {
	return ThemeTranslation{
		{Key: "readMore", Text: "続きを読む"},
		{Key: "previous", Text: "前へ"},
		{Key: "next", Text: "次へ"},
		{Key: "pageOf", Text: "{page} / {total} ページ"},
		{Key: "notFound", Text: "ページが見つかりません"},
		{Key: "postedBy", Text: "{name} が投稿"},
	}
}

var unmarshalThemeTranslationTests = []struct {
	name  string
	in    string
	want  ThemeTranslation
	valid bool // Validate accepts it
}{
	{name: "Japanese", in: jaTranslationYAML, want: jaTranslation(), valid: true},
	{
		name:  "JSON",
		in:    `{"readMore": "Read more", "pageOf": "Page {page} of {total}"}`,
		want:  ThemeTranslation{{Key: "readMore", Text: "Read more"}, {Key: "pageOf", Text: "Page {page} of {total}"}},
		valid: true,
	},
	{
		name:  "written order",
		in:    "zeta: Z\nalpha: A\nmiddle: M\n",
		want:  ThemeTranslation{{Key: "zeta", Text: "Z"}, {Key: "alpha", Text: "A"}, {Key: "middle", Text: "M"}},
		valid: true,
	},
	{
		name: "placeholders",
		in: `pageOf: '{page} / {total} ページ'
postedBy: 投稿者 {name}
range: "{from}〜{to}（全{total}件）"
twice: "{n} × {n}"
`,
		want: ThemeTranslation{
			{Key: "pageOf", Text: "{page} / {total} ページ"},
			{Key: "postedBy", Text: "投稿者 {name}"},
			{Key: "range", Text: "{from}〜{to}（全{total}件）"},
			{Key: "twice", Text: "{n} × {n}"},
		},
		valid: true,
	},
	{
		// Without quotes, these would be a number, a boolean and a comment.
		name: "quoted",
		in:   "\"404\": ページが見つかりません\n\"true\": \"true\"\nhash: \"#タグ\"\n",
		want: ThemeTranslation{
			{Key: "404", Text: "ページが見つかりません"},
			{Key: "true", Text: "true"},
			{Key: "hash", Text: "#タグ"},
		},
		valid: true,
	},
	{
		name:  "yes and no",
		in:    "yes: はい\nno: いいえ\n",
		want:  ThemeTranslation{{Key: "yes", Text: "はい"}, {Key: "no", Text: "いいえ"}},
		valid: true,
	},
	{
		// Validate reports these.
		name: "values as written",
		in: `read more: 続きを読む
"": 空
tab: "次\tへ"
nfd: Cafe` + acute + `
control: "a\x01b"
brace: 次へ }
placeholder: "{a b}"
`,
		want: ThemeTranslation{
			{Key: "read more", Text: "続きを読む"},
			{Key: "", Text: "空"},
			{Key: "tab", Text: "次\tへ"},
			{Key: "nfd", Text: "Cafe" + acute},
			{Key: "control", Text: "a" + string(rune(0x0001)) + "b"},
			{Key: "brace", Text: "次へ }"},
			{Key: "placeholder", Text: "{a b}"},
		},
	},
}

func TestUnmarshalThemeTranslation(t *testing.T) {
	for _, tt := range unmarshalThemeTranslationTests {
		t.Run(tt.name, func(t *testing.T) {
			// Every text is replaced, even if in does not have its key.
			got := jaTranslation()
			if err := got.Unmarshal([]byte(tt.in)); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", tt.in, got, tt.want)
			}
			if err := got.Validate(); (err == nil) != tt.valid {
				t.Errorf("Validate() = %v, want valid %t", err, tt.valid)
			}
		})
	}
}

var unmarshalThemeTranslationErrorTests = []struct {
	name         string
	in           string
	path, msg    string
	line, column int
}{
	// Errors in the YAML have no Path.
	{name: "empty file", in: "", msg: "empty document", line: 1, column: 1},
	{name: "only comments", in: "# 訳\n\n# まだ無い\n", msg: "empty document", line: 1, column: 1},
	{name: "duplicate key", in: "next: 次へ\nprevious: 前へ\nnext: 次\n", msg: `duplicate key "next"`, line: 3, column: 1},
	{
		name: "placeholder without quotes",
		in:   "readMore: 続きを読む\npageOf: {page} / {total} ページ\n",
		msg:  `missing value; to write a text that starts with "{", quote the whole value`, line: 2, column: 10,
	},
	{name: "hash without quotes", in: "tag: #タグ\n", msg: hashCommentMsg, line: 1, column: 1},
	{name: "null", in: "next: null\n", msg: "null is not allowed; remove the key", line: 1, column: 7},
	{name: "missing value", in: "next:\nprevious: 前へ\n", msg: "missing value", line: 1, column: 1},
	{name: "number as a key", in: "404: ページが見つかりません\n", msg: `key "404" must be a string; quote it`, line: 1, column: 1},
	{
		name: "colon in a text",
		in:   "note: 注意: 未完成\n",
		msg:  `unexpected ":"; quote the value if it is a text, or start the nested mapping on the next line`, line: 1, column: 9,
	},
	{name: "tab", in: "next:\t次へ\n", msg: "tab characters are not allowed; use spaces", line: 1, column: 6},

	{name: "sequence", in: "- 続きを読む\n- 前へ\n", msg: "expected object, got array", line: 1, column: 1},
	{name: "text", in: "続きを読む\n", msg: "expected object, got string", line: 1, column: 1},
	{name: "empty mapping", in: "{}\n", msg: "empty object is not allowed", line: 1, column: 1},
	{
		name: "nested mapping",
		in:   "readMore: 続きを読む\npagination:\n  next: 次へ\n",
		path: "/pagination", msg: "expected string, got object", line: 2, column: 1,
	},
	{name: "number", in: "count: 10\n", path: "/count", msg: "expected string, got number", line: 1, column: 1},
	{name: "boolean", in: "next: 次へ\nenabled: true\n", path: "/enabled", msg: "expected string, got boolean", line: 2, column: 1},
	{name: "sequence as a text", in: "days: [日, 月]\n", path: "/days", msg: "expected string, got array", line: 1, column: 1},
	{name: "empty string", in: "next: \"\"\n", path: "/next", msg: "empty string is not allowed", line: 1, column: 1},
	{name: "empty single-quoted string", in: "next: ''\n", path: "/next", msg: "empty string is not allowed", line: 1, column: 1},
	// Translation files have no schemaVersion: it is a key like any other.
	{name: "schemaVersion", in: "schemaVersion: 1\nnext: 次へ\n", path: "/schemaVersion", msg: "expected string, got number", line: 1, column: 1},

	// Pointers escape "~" and "/", and columns count characters.
	{name: "escaped key", in: "\"a/b~c\": [x]\n", path: "/a~1b~0c", msg: "expected string, got array", line: 1, column: 1},
	{name: "after Japanese", in: "{ readMore: 続きを読む, count: 1 }\n", path: "/count", msg: "expected string, got number", line: 1, column: 20},
	{name: "error after the other texts", in: jaTranslationYAML + "count: 1\n", path: "/count", msg: "expected string, got number", line: 8, column: 1},
}

func TestUnmarshalThemeTranslationErrors(t *testing.T) {
	for _, tt := range unmarshalThemeTranslationErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got := jaTranslation()
			err := got.Unmarshal([]byte(tt.in))
			want := &Error{Path: tt.path, Msg: tt.msg, Line: tt.line, Column: tt.column}
			var e *Error
			if !errors.As(err, &e) || *e != *want {
				t.Errorf("Unmarshal(%q) = %#v, want %#v", tt.in, err, want)
			}
			if !reflect.DeepEqual(got, jaTranslation()) {
				t.Errorf("Unmarshal(%q) changed the translation to %#v", tt.in, got)
			}
		})
	}
}

// nameMsg is the message of checkName for an invalid name s.
func nameMsg(s string) string {
	return `invalid name "` + s + `": use ASCII letters, digits, "-" and "_"`
}

var validateThemeTranslationTests = []struct {
	name string
	in   ThemeTranslation
	want []Error // Path and Msg
}{
	{name: "Japanese", in: jaTranslation()},
	{name: "empty"},
	{name: "name characters", in: ThemeTranslation{{Key: "a-Z_09", Text: "x"}, {Key: "404", Text: "y"}}},
	{name: "keys in two cases", in: ThemeTranslation{{Key: "next", Text: "次へ"}, {Key: "Next", Text: "次へ"}}},

	{name: "key", in: ThemeTranslation{{Key: "read more", Text: "続きを読む"}}, want: []Error{{Path: "/read more", Msg: nameMsg("read more")}}},
	{name: "Japanese key", in: ThemeTranslation{{Key: "続き", Text: "続きを読む"}}, want: []Error{{Path: "/続き", Msg: nameMsg("続き")}}},
	{name: "empty key", in: ThemeTranslation{{Key: "", Text: "空"}}, want: []Error{{Path: "/", Msg: "empty name"}}},
	{name: "escaped key", in: ThemeTranslation{{Key: "a/b~c", Text: "x"}}, want: []Error{{Path: "/a~1b~0c", Msg: nameMsg("a/b~c")}}},
	{
		name: "duplicate key",
		in:   ThemeTranslation{{Key: "next", Text: "次へ"}, {Key: "previous", Text: "前へ"}, {Key: "next", Text: "次"}},
		want: []Error{{Path: "/next", Msg: `duplicate key "next"`}},
	},
	{
		name: "key three times",
		in:   ThemeTranslation{{Key: "next", Text: "次へ"}, {Key: "next", Text: "次"}, {Key: "next", Text: "次へ"}},
		want: []Error{{Path: "/next", Msg: `duplicate key "next"`}, {Path: "/next", Msg: `duplicate key "next"`}},
	},
	{
		// An invalid key is not reported again as a duplicate.
		name: "invalid key twice",
		in:   ThemeTranslation{{Key: "a b", Text: "x"}, {Key: "a b", Text: "y"}},
		want: []Error{{Path: "/a b", Msg: nameMsg("a b")}, {Path: "/a b", Msg: nameMsg("a b")}},
	},

	{name: "missing text", in: ThemeTranslation{{Key: "next"}}, want: []Error{{Path: "/next", Msg: "missing text"}}},
	{
		name: "text with a tab",
		in:   ThemeTranslation{{Key: "next", Text: "次\tへ"}},
		want: []Error{{Path: "/next", Msg: "contains a line break or tab"}},
	},
	{
		name: "text not in NFC",
		in:   ThemeTranslation{{Key: "cafe", Text: "Cafe" + acute}},
		want: []Error{{Path: "/cafe", Msg: "not in Unicode NFC; normalize it first"}},
	},
	{
		name: "text with a control character",
		in:   ThemeTranslation{{Key: "next", Text: "次へ" + string(rune(0x0001))}},
		want: []Error{{Path: "/next", Msg: "contains control characters; normalize it first"}},
	},
	{
		name: "unclosed placeholder",
		in:   ThemeTranslation{{Key: "postedBy", Text: "投稿者 {name"}},
		want: []Error{{Path: "/postedBy", Msg: `unclosed "{"`}},
	},
	{
		name: "invalid placeholder",
		in:   ThemeTranslation{{Key: "pageOf", Text: "{page number}"}},
		want: []Error{{Path: "/pageOf", Msg: `invalid placeholder "{page number}": use ASCII letters, digits, "-" and "_"`}},
	},
	{
		name: "key and text",
		in:   ThemeTranslation{{Key: "a b", Text: "}"}},
		want: []Error{{Path: "/a b", Msg: nameMsg("a b")}, {Path: "/a b", Msg: `unexpected "}"`}},
	},

	{
		name: "errors in order",
		in: ThemeTranslation{
			{Key: "readMore", Text: "続きを読む"},
			{Key: "next", Text: "次へ }"},
			{Key: "a.b", Text: "x"},
			{Key: "previous"},
			{Key: "next", Text: "{}"},
		},
		want: []Error{
			{Path: "/next", Msg: `unexpected "}"`},
			{Path: "/a.b", Msg: nameMsg("a.b")},
			{Path: "/previous", Msg: "missing text"},
			{Path: "/next", Msg: `duplicate key "next"`},
			{Path: "/next", Msg: `empty placeholder "{}"`},
		},
	},
}

func TestValidateThemeTranslation(t *testing.T) {
	for _, tt := range validateThemeTranslationTests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			var es Errors
			if err != nil && !errors.As(err, &es) {
				t.Fatalf("Validate() = %v (%T), want Errors", err, err)
			}
			var got []Error
			for _, e := range es {
				got = append(got, *e)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("Validate() = %v, want %v", got, tt.want)
			}
		})
	}
}

// placeholderMsg is the message for the invalid placeholder s.
func placeholderMsg(s string) string {
	return `invalid placeholder "` + s + `": use ASCII letters, digits, "-" and "_"`
}

var themeTextTests = []struct {
	name  string
	in    string
	msg   string   // "" if valid
	names []string // the placeholders of a valid text
}{
	{name: "no placeholders", in: "続きを読む"},
	{name: "one", in: "{name} が投稿", names: []string{"name"}},
	{name: "several", in: "{page} / {total} ページ", names: []string{"page", "total"}},
	{name: "repeated", in: "{n} × {n} = {n}²", names: []string{"n"}},
	{name: "order of first appearance", in: "{b}{a}{b}{c}{a}", names: []string{"b", "a", "c"}},
	{name: "only a placeholder", in: "{x}", names: []string{"x"}},
	{name: "name characters", in: "{a-Z_09}", names: []string{"a-Z_09"}},
	{name: "digits", in: "{0}, {1}", names: []string{"0", "1"}},
	{name: "names in two cases", in: "{Page} {page}", names: []string{"Page", "page"}},
	{name: "surrounding spaces", in: " {a} ", names: []string{"a"}},
	{name: "ideographic space", in: "前へ" + string(rune(0x3000)) + "{n}", names: []string{"n"}},
	{name: "fullwidth braces", in: string(rune(0xFF5B)) + "page" + string(rune(0xFF5D))},

	// Lines are checked before braces.
	{name: "tab", in: "次\tへ", msg: "contains a line break or tab"},
	{name: "line break", in: "次\nへ", msg: "contains a line break or tab"},
	{name: "line separator", in: "次" + string(rune(0x2028)) + "へ", msg: "contains a line break or tab"},
	{name: "tab and a brace", in: "}\t", msg: "contains a line break or tab"},
	{name: "control character", in: "次" + string(rune(0x0001)) + "へ", msg: "contains control characters; normalize it first"},
	{name: "DEL", in: "a\x7fb", msg: "contains control characters; normalize it first"},
	{name: "NFD", in: "Cafe" + acute, msg: "not in Unicode NFC; normalize it first"},
	{name: "NFD in a placeholder", in: "{e" + acute + "}", msg: "not in Unicode NFC; normalize it first"},
	{name: "invalid UTF-8", in: "a\xffb", msg: "invalid UTF-8"},
	{name: "too long", in: strings.Repeat("a", MaxTextBytes+1), msg: "too long: 1048577 bytes (max 1048576)"},

	// Braces cannot be written as text.
	{name: "closing brace", in: "}", msg: `unexpected "}"`},
	{name: "closing brace after a text", in: "次へ }", msg: `unexpected "}"`},
	{name: "closing brace after a name", in: "page}", msg: `unexpected "}"`},
	{name: "closing brace before a placeholder", in: "}{page}", msg: `unexpected "}"`},
	{name: "closing brace after a placeholder", in: "{page}}", msg: `unexpected "}"`},
	{name: "braces in the wrong order", in: "}{", msg: `unexpected "}"`},
	{name: "opening brace", in: "{", msg: `unclosed "{"`},
	{name: "unclosed placeholder", in: "投稿者 {name", msg: `unclosed "{"`},
	{name: "unclosed after a placeholder", in: "{page} / {total", msg: `unclosed "{"`},
	{name: "empty placeholder", in: "{}", msg: `empty placeholder "{}"`},
	{name: "empty placeholder after a placeholder", in: "{a}{}", msg: `empty placeholder "{}"`},
	{name: "space in a placeholder", in: "{a b}", msg: placeholderMsg("{a b}")},
	{name: "spaces around a name", in: "{ page }", msg: placeholderMsg("{ page }")},
	{name: "Japanese name", in: "{ページ}", msg: placeholderMsg("{ページ}")},
	{name: "dot in a name", in: "{page.number}", msg: placeholderMsg("{page.number}")},
	{name: "double braces", in: "{{page}}", msg: placeholderMsg("{{page}")},
	{name: "brace in a placeholder", in: "{a{b}", msg: placeholderMsg("{a{b}")},
	{name: "first problem", in: "{a b} }", msg: placeholderMsg("{a b}")},
}

func TestThemeText(t *testing.T) {
	for _, tt := range themeTextTests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorMsg(checkThemeText(tt.in)); got != tt.msg {
				t.Errorf("checkThemeText(%q) = %q, want %q", tt.in, got, tt.msg)
			}
			names, ok := themeTextOK(tt.in)
			if ok != (tt.msg == "") {
				t.Errorf("themeTextOK(%q) = %t", tt.in, ok)
			}
			if tt.msg != "" {
				return
			}
			if got := (ThemeText{Key: "k", Text: tt.in}).Placeholders(); !slices.Equal(got, tt.names) {
				t.Errorf("Placeholders() of %q = %q, want %q", tt.in, got, tt.names)
			}
			if !slices.Equal(names, tt.names) {
				t.Errorf("themeTextOK(%q) = %q, want %q", tt.in, names, tt.names)
			}
		})
	}
}

var placeholderPattern = regexp.MustCompile(`\{([-_0-9A-Za-z]+)\}`)

// themeTextOK is a simple reference for checkThemeText and Placeholders: a valid text is a line without braces after
// removing its placeholders. It also returns the names of the placeholders in order of first appearance.
func themeTextOK(s string) ([]string, bool) {
	if checkLine(s) != nil || strings.ContainsAny(placeholderPattern.ReplaceAllString(s, ""), "{}") {
		return nil, false
	}
	var names []string
	for _, m := range placeholderPattern.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	return names, true
}

// FuzzThemeText checks that checkThemeText accepts exactly the texts that themeTextOK accepts, and that Placeholders
// returns the names that themeTextOK returns for them.
func FuzzThemeText(f *testing.F) {
	for _, tt := range themeTextTests {
		if len(tt.in) <= 1000 {
			f.Add(tt.in)
		}
	}
	f.Fuzz(func(t *testing.T, s string) {
		err := checkThemeText(s)
		got := ThemeText{Text: s}.Placeholders()
		names, ok := themeTextOK(s)
		if ok != (err == nil) {
			t.Fatalf("checkThemeText(%q) = %v, but themeTextOK = %t", s, err, ok)
		}
		if ok && !slices.Equal(got, names) {
			t.Errorf("Placeholders() of %q = %q, want %q", s, got, names)
		}
	})
}

const translationFile = "translations/ja.yaml"

func TestLoadThemeTranslation(t *testing.T) {
	got, err := LoadThemeTranslation(translationFile, []byte(jaTranslationYAML))
	if err != nil {
		t.Fatalf("LoadThemeTranslation() = %v", err)
	}
	if !reflect.DeepEqual(got, jaTranslation()) {
		t.Errorf("LoadThemeTranslation() = %#v, want %#v", got, jaTranslation())
	}
}

var loadThemeTranslationErrorTests = []struct {
	name string
	in   string
	want string // Error() of the error
}{
	// Errors of reading the file, without validation.
	{name: "empty file", in: "", want: "translations/ja.yaml:1:1: empty document"},
	{
		name: "placeholder without quotes",
		in:   "readMore: 続きを読む\npageOf: {page} / {total} ページ\n",
		want: `translations/ja.yaml:2:10: missing value; to write a text that starts with "{", quote the whole value`,
	},
	{name: "number", in: "next: 次へ\ncount: 10\n", want: "translations/ja.yaml:2:1: /count: expected string, got number"},
	{
		// The invalid key is not validated.
		name: "error of reading after an invalid key",
		in:   "read more: 続きを読む\nnext: ''\n",
		want: "translations/ja.yaml:2:1: /next: empty string is not allowed",
	},

	// Errors of Validate, at their keys.
	{
		name: "invalid key",
		in:   "readMore: 続きを読む\nread more: 続き\n",
		want: "translations/ja.yaml:2:1: /read more: " + nameMsg("read more"),
	},
	{name: "unclosed placeholder", in: "next: 次へ\npostedBy: 投稿者 {name\n", want: `translations/ja.yaml:2:1: /postedBy: unclosed "{"`},
	{name: "tab", in: `next: "次\tへ"` + "\n", want: "translations/ja.yaml:1:1: /next: contains a line break or tab"},
	{
		name: "NFD",
		in:   "next: 次へ\ncafe: Cafe" + acute + "\n",
		want: "translations/ja.yaml:2:1: /cafe: not in Unicode NFC; normalize it first",
	},
	{
		name: "in a flow mapping",
		in:   "{ readMore: 続きを読む, pageOf: '{page' }\n",
		want: `translations/ja.yaml:1:20: /pageOf: unclosed "{"`,
	},
	{name: "escaped key", in: "\"a/b\": x\n", want: "translations/ja.yaml:1:1: /a~1b: " + nameMsg("a/b")},
	{
		name: "errors in order",
		in:   "a b: \"}\"\nnext: 次へ\n\"\": \"{}\"\n",
		want: "translations/ja.yaml:1:1: /a b: " + nameMsg("a b") + "\n" +
			`translations/ja.yaml:1:1: /a b: unexpected "}"` + "\n" +
			"translations/ja.yaml:3:1: /: empty name\n" +
			`translations/ja.yaml:3:1: /: empty placeholder "{}"`,
	},
}

func TestLoadThemeTranslationErrors(t *testing.T) {
	for _, tt := range loadThemeTranslationErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadThemeTranslation(translationFile, []byte(tt.in))
			var es Errors
			if !errors.As(err, &es) || err.Error() != tt.want {
				t.Errorf("LoadThemeTranslation() = %q (%T), want %q", err, err, tt.want)
			}
			if got != nil {
				t.Errorf("LoadThemeTranslation() = %#v with an error", got)
			}
		})
	}
}

// FuzzLoadThemeTranslation checks that LoadThemeTranslation returns the translation that Unmarshal reads and Validate
// accepts, whose texts have exactly the braces of the placeholders that Placeholders names, or nil and the error of
// Unmarshal or the errors of Validate, each with its file and a position in it.
func FuzzLoadThemeTranslation(f *testing.F) {
	for _, tt := range unmarshalThemeTranslationTests {
		f.Add([]byte(tt.in))
	}
	for _, tt := range unmarshalThemeTranslationErrorTests {
		f.Add([]byte(tt.in))
	}
	for _, tt := range loadThemeTranslationErrorTests {
		f.Add([]byte(tt.in))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		x, err := LoadThemeTranslation(translationFile, data)
		read := jaTranslation()
		readErr := read.Unmarshal(data)
		if readErr != nil && !reflect.DeepEqual(read, jaTranslation()) {
			t.Fatalf("Unmarshal(%q) = %v and changed the translation to %#v", data, readErr, read)
		}
		if err == nil {
			if len(x) == 0 || readErr != nil || !reflect.DeepEqual(x, read) {
				t.Fatalf("LoadThemeTranslation(%q) = %#v, but Unmarshal = %#v, %v", data, x, read, readErr)
			}
			if err := x.Validate(); err != nil {
				t.Fatalf("Validate() of the translation of LoadThemeTranslation(%q) = %v", data, err)
			}
			for _, text := range x {
				names := text.Placeholders()
				rest := text.Text
				for i, name := range names {
					if checkName(name) != nil || slices.Contains(names[:i], name) || !strings.Contains(rest, "{"+name+"}") {
						t.Errorf("Placeholders() of %q = %q", text.Text, names)
					}
					rest = strings.ReplaceAll(rest, "{"+name+"}", "")
				}
				if strings.ContainsAny(rest, "{}") {
					t.Errorf("Placeholders() of %q = %q, without every placeholder", text.Text, names)
				}
			}
			return
		}
		var es Errors
		if x != nil || !errors.As(err, &es) || len(es) == 0 {
			t.Fatalf("LoadThemeTranslation(%q) = %#v, %v (%T); want nil and Errors", data, x, err, err)
		}
		lines := strings.Split(string(data), "\n")
		for _, e := range es {
			if e.File != translationFile || e.Line < 1 || e.Line > len(lines) || e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
				t.Errorf("LoadThemeTranslation(%q) = %v, outside the file", data, e)
			}
		}
		if readErr != nil {
			var re *Error
			if !errors.As(readErr, &re) || len(es) != 1 || es[0].Path != re.Path || es[0].Msg != re.Msg ||
				es[0].Line != re.Line || es[0].Column != re.Column {
				t.Errorf("LoadThemeTranslation(%q) = %v, but Unmarshal = %v", data, err, readErr)
			}
			return
		}
		var want Errors
		if !errors.As(read.Validate(), &want) || len(es) != len(want) {
			t.Fatalf("LoadThemeTranslation(%q) = %v, but Validate() = %v", data, err, want)
		}
		for i, e := range es {
			if e.Path != want[i].Path || e.Msg != want[i].Msg {
				t.Errorf("LoadThemeTranslation(%q) = %v, but Validate() = %v", data, e, want[i])
			}
		}
	})
}
