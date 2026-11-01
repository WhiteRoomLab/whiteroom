package content

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

func at(line, column int) yaml.Position {
	return yaml.Position{Line: line, Column: column}
}

const grinningFace = string(rune(0x1F600))

const positionsDoc = `{
  "schemaVersion": 1,
  "name": {"ja": "白い部屋", "en": "White Room"},
  "languages": ["ja", "en"],
  "seo": {
    "titleFormat": {
      "ja": "{title} | {siteName}"
    },
    "noindex": true
  },
  "postsPerPage": 10
}
`

var positionsDocWant = map[string]yaml.Position{
	"":                    at(1, 1),
	"/schemaVersion":      at(2, 3),
	"/name":               at(3, 3),
	"/name/ja":            at(3, 12),
	"/name/en":            at(3, 26),
	"/languages":          at(4, 3),
	"/languages/0":        at(4, 17),
	"/languages/1":        at(4, 23),
	"/seo":                at(5, 3),
	"/seo/titleFormat":    at(6, 5),
	"/seo/titleFormat/ja": at(7, 7),
	"/seo/noindex":        at(9, 5),
	"/postsPerPage":       at(11, 3),
}

var jsonPositionsTests = []struct {
	name    string
	in      string
	want    map[string]yaml.Position
	notYAML bool // outside the YAML that yaml.ToJSON reads
}{
	{name: "nested", in: positionsDoc, want: positionsDocWant},
	{name: "CRLF", in: strings.ReplaceAll(positionsDoc, "\n", "\r\n"), want: positionsDocWant},
	{
		name: "Japanese",
		in:   `{"名前": {"日本語": "値", "英語": ["一", "二"]}}`,
		want: map[string]yaml.Position{
			"": at(1, 1), "/名前": at(1, 2), "/名前/日本語": at(1, 9), "/名前/英語": at(1, 21),
			"/名前/英語/0": at(1, 28), "/名前/英語/1": at(1, 33),
		},
	},
	{
		name: "characters, not bytes",
		in:   `{"` + grinningFace + `": 1, "e` + acute + `": 2, "c": 3}`,
		want: map[string]yaml.Position{"": at(1, 1), "/" + grinningFace: at(1, 2), "/e" + acute: at(1, 10), "/c": at(1, 19)},
	},
	{
		name: "escapes as written",
		in:   `{"a": "\"白\"", "b": 1}`,
		want: map[string]yaml.Position{"": at(1, 1), "/a": at(1, 2), "/b": at(1, 16)},
	},
	{
		name: "spaces and line breaks between tokens",
		in: "\n" + `  {
  "a"
    :
      [
        1 ,
        { "b" : true }
      ]
}
`,
		want: map[string]yaml.Position{"": at(2, 3), "/a": at(3, 3), "/a/0": at(6, 9), "/a/1": at(7, 9), "/a/1/b": at(7, 11)},
	},
	{
		name:    "tabs",
		in:      "{\t\"a\":\t[\t1]}",
		want:    map[string]yaml.Position{"": at(1, 1), "/a": at(1, 3), "/a/0": at(1, 10)},
		notYAML: true,
	},
	{
		name: "arrays",
		in:   `[[1,[2]],[],{"a":[{}]}]`,
		want: map[string]yaml.Position{
			"": at(1, 1), "/0": at(1, 2), "/0/0": at(1, 3), "/0/1": at(1, 5), "/0/1/0": at(1, 6),
			"/1": at(1, 10), "/2": at(1, 13), "/2/a": at(1, 14), "/2/a/0": at(1, 19),
		},
	},
	{
		name: "empty object and array",
		in:   `{"a": {}, "b": []}`,
		want: map[string]yaml.Position{"": at(1, 1), "/a": at(1, 2), "/b": at(1, 11)},
	},
	{
		name: "escaped pointers",
		in:   `{"a/b": {"~c": 1}, "d\/e": 2}`,
		want: map[string]yaml.Position{"": at(1, 1), "/a~1b": at(1, 2), "/a~1b/~0c": at(1, 10), "/d~1e": at(1, 20)},
	},
	{
		name: "empty keys",
		in:   `{"": {"": 1}}`,
		want: map[string]yaml.Position{"": at(1, 1), "/": at(1, 2), "//": at(1, 7)},
	},
	{name: "string", in: `"x"`, want: map[string]yaml.Position{"": at(1, 1)}},
	{name: "number after spaces", in: "\n  42\n", want: map[string]yaml.Position{"": at(2, 3)}},
	{name: "boolean", in: "true", want: map[string]yaml.Position{"": at(1, 1)}},
	{name: "empty object", in: "{}", want: map[string]yaml.Position{"": at(1, 1)}},
}

func TestJSONPositions(t *testing.T) {
	for _, tt := range jsonPositionsTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := jsonPositions([]byte(tt.in))
			if err != nil {
				t.Fatalf("jsonPositions(%q) = %v", tt.in, err)
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("jsonPositions(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if tt.notYAML {
				return
			}
			_, positions, err := yaml.ToJSON([]byte(tt.in))
			if err != nil {
				t.Fatalf("yaml.ToJSON(%q) = %v", tt.in, err)
			}
			if !maps.Equal(positions, tt.want) {
				t.Errorf("yaml.ToJSON(%q) has the positions %v, want %v", tt.in, positions, tt.want)
			}
		})
	}
}

var jsonPositionsErrorTests = []struct {
	name         string
	in           string
	line, column int
	msg          string
}{
	{name: "empty", in: "", line: 1, column: 1, msg: "unexpected end of JSON input"},
	{name: "only spaces", in: "\n \n  ", line: 3, column: 3, msg: "unexpected end of JSON input"},
	{name: "truncated array", in: `{"a": [1,`, line: 1, column: 10, msg: "unexpected end of JSON input"},
	{name: "truncated string", in: `{"a": "白い`, line: 1, column: 10, msg: "unexpected end of JSON input"},
	{name: "truncated after a line break", in: "{\n  \"a\": {\n", line: 3, column: 1, msg: "unexpected end of JSON input"},
	{name: "truncated number", in: `{"a": -`, line: 1, column: 7, msg: "unexpected end of JSON input"},
	{name: "invalid character", in: `{"a": x}`, line: 1, column: 7, msg: "invalid character 'x' at start of value"},
	{
		name: "invalid character after Japanese",
		in:   "{\n  \"名前\": 白}",
		line: 2, column: 9,
		msg: "invalid character '白' at start of value",
	},
	{
		name: "invalid literal",
		in:   "{\n  \"a\": tru\n}",
		line: 2, column: 11,
		msg: `invalid character '\n' in literal true (expecting 'e')`,
	},
	{
		name: "missing colon",
		in:   "{\"a\"\n  1}",
		line: 2, column: 3,
		msg: "invalid character '1' after object name (expecting ':')",
	},
	{name: "trailing comma", in: "{\n  \"a\": 1,\n}", line: 2, column: 9, msg: "invalid character ',' at start of value"},
	{
		name: "mismatched bracket",
		in:   `{"a": 1]`,
		line: 1, column: 8,
		msg: `invalid character ']' after object value (expecting ',' or '}')`,
	},
	{name: "number as a key", in: `{1: 2}`, line: 1, column: 2, msg: "object member name must be a string"},
	{
		name: "control character",
		in:   "{\"a\": \"x\x01\"}",
		line: 1, column: 9,
		msg: `invalid character '\x01' in string (expecting non-control character)`,
	},
	{name: "invalid UTF-8", in: "{\"名\": \"\xff\"}", line: 1, column: 8, msg: "invalid UTF-8"},
	{
		name: "byte order mark",
		in:   "\xef\xbb\xbf{}",
		line: 1, column: 1,
		msg: "invalid character " + strconv.QuoteRune(0xFEFF) + " at start of value",
	},
	{name: "duplicate key", in: "{\n  \"a\": 1,\n  \"a\": 2\n}", line: 3, column: 3, msg: "duplicate key"},
	{name: "duplicate escaped key", in: `{"o": {"/": 1, "\/": 2}}`, line: 1, column: 16, msg: "duplicate key"},
	{name: "duplicate key after CRLF", in: "{\r\n  \"a\": 1,\r\n  \"a\": 2\r\n}", line: 3, column: 3, msg: "duplicate key"},
	{name: "second value", in: `{"a": 1} {}`, line: 1, column: 10, msg: "unexpected data after value"},
	{name: "extra brace", in: `{"a": 1}}`, line: 1, column: 9, msg: "unexpected data after value"},
	{name: "extra data after lines", in: "{}\n\n  x", line: 3, column: 3, msg: "unexpected data after value"},
	{name: "second string", in: `"a" "b"`, line: 1, column: 5, msg: "unexpected data after value"},
	{name: "comma after the value", in: "[1],", line: 1, column: 4, msg: "unexpected data after value"},
	{name: "invalid UTF-8 after the value", in: "{} \xff", line: 1, column: 4, msg: "unexpected data after value"},
}

func TestJSONPositionsError(t *testing.T) {
	for _, tt := range jsonPositionsErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := jsonPositions([]byte(tt.in))
			want := &Error{Msg: tt.msg, Line: tt.line, Column: tt.column}
			var e *Error
			if !errors.As(err, &e) || *e != *want {
				t.Errorf("jsonPositions(%q) = %#v, want %#v", tt.in, err, want)
			}
			if got != nil {
				t.Errorf("jsonPositions(%q) = %v with an error", tt.in, got)
			}
		})
	}
}

func TestLocate(t *testing.T) {
	const file = "site/schema.yaml"
	positions := map[string]yaml.Position{
		"":                           at(1, 1),
		"/types":                     at(2, 1),
		"/types/works":               at(3, 3),
		"/types/works/fields":        at(4, 5),
		"/types/works/fields/client": at(5, 7),
		"/a~1b":                      at(9, 3),
		"/":                          at(10, 3),
	}
	tests := []struct {
		name      string
		err       error
		file      string
		positions map[string]yaml.Position
		want      Errors
	}{
		{name: "nil", file: file, positions: positions},
		{
			name: "pointer",
			err:  &Error{Path: "/types/works", Msg: "m"},
			file: file, positions: positions,
			want: Errors{{Path: "/types/works", Msg: "m", File: file, Line: 3, Column: 3}},
		},
		{
			name: "parent",
			err:  &Error{Path: "/types/works/fields/client/type", Msg: "missing type"},
			file: file, positions: positions,
			want: Errors{{Path: "/types/works/fields/client/type", Msg: "missing type", File: file, Line: 5, Column: 7}},
		},
		{
			name: "ancestor",
			err:  &Error{Path: "/types/works/fields/client/of/fields/x", Msg: "m"},
			file: file, positions: positions,
			want: Errors{{Path: "/types/works/fields/client/of/fields/x", Msg: "m", File: file, Line: 5, Column: 7}},
		},
		{
			name: "root",
			err:  &Error{Msg: `missing key "schemaVersion"`},
			file: file, positions: positions,
			want: Errors{{Msg: `missing key "schemaVersion"`, File: file, Line: 1, Column: 1}},
		},
		{
			name: "up to the root",
			err:  &Error{Path: "/name/ja", Msg: "m"},
			file: file, positions: positions,
			want: Errors{{Path: "/name/ja", Msg: "m", File: file, Line: 1, Column: 1}},
		},
		{
			name: "escaped slash",
			err:  &Error{Path: "/a~1b/c", Msg: "m"},
			file: file, positions: positions,
			want: Errors{{Path: "/a~1b/c", Msg: "m", File: file, Line: 9, Column: 3}},
		},
		{
			name: "escaped slash in a key",
			err:  &Error{Path: "/a~1b~1c", Msg: "m"},
			file: file, positions: positions,
			want: Errors{{Path: "/a~1b~1c", Msg: "m", File: file, Line: 1, Column: 1}},
		},
		{
			name: "empty key",
			err:  &Error{Path: "//x", Msg: "m"},
			file: file, positions: positions,
			want: Errors{{Path: "//x", Msg: "m", File: file, Line: 10, Column: 3}},
		},
		{
			name: "syntax error",
			err:  &Error{Msg: "duplicate key", Line: 7, Column: 2},
			file: file, positions: positions,
			want: Errors{{Msg: "duplicate key", File: file, Line: 7, Column: 2}},
		},
		{
			name: "position kept",
			err:  &Error{Path: "/types", Msg: "m", Line: 8, Column: 4},
			file: file, positions: positions,
			want: Errors{{Path: "/types", Msg: "m", File: file, Line: 8, Column: 4}},
		},
		{
			name: "without positions",
			err:  &Error{Path: "/a", Msg: "m"},
			file: file,
			want: Errors{{Path: "/a", Msg: "m", File: file}},
		},
		{
			name: "without the root",
			err:  &Error{Path: "/b/c", Msg: "m"},
			file: file, positions: map[string]yaml.Position{"/a": at(2, 1)},
			want: Errors{{Path: "/b/c", Msg: "m", File: file}},
		},
		{
			name: "without a file",
			err:  &Error{Path: "/types", Msg: "m"},
			file: "", positions: positions,
			want: Errors{{Path: "/types", Msg: "m", Line: 2, Column: 1}},
		},
		{
			name: "errors in order",
			err:  Errors{{Path: "/types/works/url", Msg: "a"}, {Msg: "b", Line: 6, Column: 9}, {Path: "/x", Msg: "c"}},
			file: file, positions: positions,
			want: Errors{
				{Path: "/types/works/url", Msg: "a", File: file, Line: 3, Column: 3},
				{Msg: "b", File: file, Line: 6, Column: 9},
				{Path: "/x", Msg: "c", File: file, Line: 1, Column: 1},
			},
		},
		{
			name: "other error",
			err:  errors.New("boom"),
			file: file, positions: positions,
			want: Errors{{Msg: "boom", File: file, Line: 1, Column: 1}},
		},
		{
			name: "wrapped error",
			err:  fmt.Errorf("reading: %w", &Error{Path: "/types", Msg: "m"}),
			file: file, positions: positions,
			want: Errors{{Path: "/types", Msg: "m", File: file, Line: 2, Column: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in Errors
			var e *Error
			switch {
			case errors.As(tt.err, &in):
			case errors.As(tt.err, &e):
				in = Errors{e}
			}
			before := make([]Error, len(in))
			for i, e := range in {
				before[i] = *e
			}
			got := locate(tt.err, tt.file, tt.positions)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("locate() = %#v, want %#v", got, tt.want)
			}
			for i, e := range in {
				if *e != before[i] {
					t.Errorf("locate() changed the error %d to %#v", i, *e)
				}
				if slices.Contains(got, e) {
					t.Errorf("locate() returned the error %d, not a copy", i)
				}
			}
		})
	}
}

func TestLocateUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "unknown key", in: "{\n  \"o\": {\n    \"x\": 1\n  }\n}", want: "settings.json:3:5: /o/x: unknown key"},
		{name: "value of a member", in: "{\n  \"s\":\n    1\n}", want: "settings.json:2:3: /s: expected string, got number"},
		{name: "empty object", in: "{\n  \"o\": {}\n}", want: "settings.json:2:3: /o: empty object is not allowed"},
		{name: "item", in: `{"a": ["x", 1]}`, want: "settings.json:1:13: /a/1: expected string, got number"},
		{name: "escaped key", in: `{"o": {"a/b": 1}}`, want: "settings.json:1:8: /o/a~1b: unknown key"},
		{name: "after Japanese", in: `{"s": "白い部屋", "o": {"x": 1}}`, want: "settings.json:1:21: /o/x: unknown key"},
		{name: "root", in: "\n null", want: "settings.json:2:2: expected object, got null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			positions, err := jsonPositions([]byte(tt.in))
			if err != nil {
				t.Fatalf("jsonPositions(%q) = %v", tt.in, err)
			}
			var p probe
			got := locate(unmarshal([]byte(tt.in), "", p.decode), "settings.json", positions)
			if got.Error() != tt.want {
				t.Errorf("errors = %q, want %q", got.Error(), tt.want)
			}
		})
	}
}

func TestDecodeFile(t *testing.T) {
	const name = "entries/" + testAboutPage + ".json"
	tests := []struct {
		name   string
		in     string
		want   string // Error() of the error; "" for none
		decode bool   // the decode function is called
	}{
		{name: "valid", in: "{\n  \"schemaVersion\": 1,\n  \"number\": 2\n}\n", decode: true},
		{
			name:   "unknown key",
			in:     "{\n  \"schemaVersion\": 1,\n  \"numbr\": 2\n}\n",
			want:   name + ":3:3: /numbr: unknown key",
			decode: true,
		},
		{
			name:   "missing key at the root",
			in:     "\n{\n  \"number\": 2\n}\n",
			want:   name + `:2:1: missing key "schemaVersion"`,
			decode: true,
		},
		{
			name: "syntax error",
			in:   "{\n  \"schemaVersion\": 1\n  \"number\": 2\n}\n",
			want: name + `:3:3: invalid character '"' after object value (expecting ',' or '}')`,
		},
		{name: "duplicate key", in: "{\n  \"number\": 1,\n  \"number\": 2\n}\n", want: name + ":3:3: duplicate key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e Entry
			decoded := false
			err := DecodeFile(SiteFile{Name: name, Data: []byte(tt.in)}, func(data []byte) error {
				decoded = true
				return e.UnmarshalJSON(data)
			})
			if decoded != tt.decode {
				t.Errorf("DecodeFile() called decode: %t, want %t", decoded, tt.decode)
			}
			if tt.want == "" {
				if err != nil || e.Number != 2 {
					t.Errorf("DecodeFile() = %v and the number %d, want nil and 2", err, e.Number)
				}
				return
			}
			var es Errors
			if !errors.As(err, &es) || err.Error() != tt.want {
				t.Errorf("DecodeFile() = %q (%T), want %q", err, err, tt.want)
			}
			if !tt.decode && len(es) == 1 && es[0].Path != "" {
				t.Errorf("DecodeFile() = %#v, want a syntax error without a Path", es[0])
			}
		})
	}
}

func TestLocateErrors(t *testing.T) {
	const name = "revisions/" + testRevision + ".json"
	doc := []byte("{\n  \"schemaVersion\": 1,\n  \"seo\": {\n    \"noindex\": true\n  }\n}\n")
	tests := []struct {
		name string
		err  error
		data []byte
		want string // Error() of the errors; "" for nil
	}{
		{name: "nil", data: doc},
		{name: "member", err: &Error{Path: "/seo/noindex", Msg: "m"}, data: doc, want: name + ":4:5: /seo/noindex: m"},
		{
			name: "missing member at its parent",
			err:  &Error{Path: "/seo/title", Msg: "missing title"},
			data: doc,
			want: name + ":3:3: /seo/title: missing title",
		},
		{
			name: "errors in order",
			err:  Errors{{Path: "/title", Msg: "missing title"}, {Path: "/schemaVersion", Msg: "m"}},
			data: doc,
			want: name + ":1:1: /title: missing title\n" + name + ":2:3: /schemaVersion: m",
		},
		{name: "position kept", err: &Error{Path: "/seo", Msg: "m", Line: 9, Column: 2}, data: doc, want: name + ":9:2: /seo: m"},
		{name: "other error", err: errors.New("boom"), data: doc, want: name + ":1:1: boom"},
		{
			name: "not JSON",
			err:  &Error{Path: "/seo/title", Msg: "missing title"},
			data: []byte("{\n  \"seo\": {"),
			want: name + ": /seo/title: missing title",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := fmt.Sprint(tt.err)
			got := LocateErrors(tt.err, SiteFile{Name: name, Data: tt.data})
			if tt.want == "" {
				if got != nil {
					t.Errorf("LocateErrors() = %#v, want nil", got)
				}
				return
			}
			if got.Error() != tt.want {
				t.Errorf("LocateErrors() = %q, want %q", got.Error(), tt.want)
			}
			if after := fmt.Sprint(tt.err); after != before {
				t.Errorf("LocateErrors() changed the error to %q", after)
			}
		})
	}
}

// TestLocateErrorsYAML checks that LocateErrors locates the errors about the schema file, which is YAML, as LoadSite
// does.
func TestLocateErrorsYAML(t *testing.T) {
	const name = "site/schema.yaml"
	schema := []byte("# the types\nschemaVersion: 1\ntypes:\n  works:\n    url: /works/{slug}/\n    archive: /works/\n")
	tests := []struct {
		name string
		err  error
		data []byte
		want string // Error() of the errors
	}{
		{name: "member", err: &Error{Path: "/types/works/archive", Msg: "m"}, data: schema, want: name + ":6:5: /types/works/archive: m"},
		{
			name: "missing member at its parent",
			err:  &Error{Path: "/types/works/label", Msg: "m"},
			data: schema,
			want: name + ":4:3: /types/works/label: m",
		},
		{name: "root", err: errors.New("boom"), data: schema, want: name + ":2:1: boom"},
		{name: "neither JSON nor YAML", err: &Error{Path: "/types", Msg: "m"}, data: []byte("types:\n\tworks: {}\n"), want: name + ": /types: m"},
		{name: "empty", err: &Error{Path: "/types", Msg: "m"}, want: name + ": /types: m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LocateErrors(tt.err, SiteFile{Name: name, Data: tt.data}); got.Error() != tt.want {
				t.Errorf("LocateErrors() = %q, want %q", got.Error(), tt.want)
			}
		})
	}
}

// offsetAt returns the byte offset of pos in data.
func offsetAt(data []byte, pos yaml.Position) int {
	off := 0
	for range pos.Line - 1 {
		off += bytes.IndexByte(data[off:], '\n') + 1
	}
	for range pos.Column - 1 {
		_, n := utf8.DecodeRune(data[off:])
		off += n
	}
	return off
}

// firstToken is the token at the position of a value: the key of a member, or else the value.
type firstToken struct {
	kind   jsontext.Kind
	member bool
	key    string
}

// firstTokens adds the first token of v, and of each value in it, to tokens by JSON Pointer.
func firstTokens(v any, ptr string, tokens map[string]firstToken) {
	tok := firstToken{kind: 'n'}
	switch v := v.(type) {
	case map[string]any:
		tok.kind = '{'
		for k, x := range v {
			p := ptr + "/" + escapePointer(k)
			firstTokens(x, p, tokens)
			tokens[p] = firstToken{kind: '"', member: true, key: k}
		}
	case []any:
		tok.kind = '['
		for i, x := range v {
			firstTokens(x, ptr+"/"+strconv.Itoa(i), tokens)
		}
	case string:
		tok.kind = '"'
	case float64:
		tok.kind = '0'
	case bool:
		tok.kind = 'f'
		if v {
			tok.kind = 't'
		}
	}
	tokens[ptr] = tok
}

func FuzzJSONPositions(f *testing.F) {
	for _, tt := range jsonPositionsTests {
		f.Add([]byte(tt.in))
	}
	for _, tt := range jsonPositionsErrorTests {
		f.Add([]byte(tt.in))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		positions, err := jsonPositions(data)
		valid := jsontext.Value(data).IsValid()
		if err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Path != "" || e.File != "" || positions != nil {
				t.Fatalf("jsonPositions(%q) = %v, %#v; want an *Error without a Path", data, positions, err)
			}
			lines := strings.Split(string(data), "\n")
			if e.Line < 1 || e.Line > len(lines) || e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
				t.Errorf("jsonPositions(%q) = %v, outside the input", data, err)
			}
			if valid {
				t.Errorf("jsonPositions(%q) = %v for valid JSON", data, err)
			}
			return
		}
		if !valid {
			t.Fatalf("jsonPositions(%q) = %v for invalid JSON", data, positions)
		}
		if _, ypos, err := yaml.ToJSON(data); err == nil && !maps.Equal(ypos, positions) {
			t.Errorf("jsonPositions(%q) = %v, but yaml.ToJSON has %v", data, positions, ypos)
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Skip(err) // a number out of the range of float64
		}
		want := make(map[string]firstToken)
		firstTokens(v, "", want)
		if len(positions) != len(want) {
			t.Errorf("jsonPositions(%q) = %v, want the %d values", data, positions, len(want))
		}
		for ptr, w := range want {
			pos, ok := positions[ptr]
			if !ok {
				t.Errorf("jsonPositions(%q) has no %q", data, ptr)
				continue
			}
			tok, err := jsontext.NewDecoder(bytes.NewBuffer(data[offsetAt(data, pos):])).ReadToken()
			if err != nil || tok.Kind() != w.kind || w.member && tok.String() != w.key {
				t.Errorf("jsonPositions(%q) has %q at %v, where %v is", data, ptr, pos, tok)
			}
		}
	})
}
