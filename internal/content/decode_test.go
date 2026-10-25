package content

import (
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func assertError(t *testing.T, err error, path, msg string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v (%T), want *Error %q at %q", err, err, msg, path)
	}
	if e.Path != path || !strings.Contains(e.Msg, msg) {
		t.Errorf("error = %q at %q, want %q at %q", e.Msg, e.Path, msg, path)
	}
}

type probe struct {
	S    string
	B    bool
	N    string
	O    *probe
	A, I []string
	R    jsontext.Value
	RP   string
}

func (p *probe) decode(d *decoder) error {
	return d.object(func(key string) error {
		var err error
		switch key {
		case "s":
			p.S, err = d.string()
		case "b":
			p.B, err = d.bool()
		case "n":
			p.N, err = d.number()
		case "o":
			p.O = new(probe)
			err = p.O.decode(d)
		case "a":
			err = d.array(func(int) error {
				s, err := d.string()
				p.A = append(p.A, s)
				return err
			})
		case "i":
			p.I = []string{}
			err = d.items(func(int) error {
				s, err := d.string()
				p.I = append(p.I, s)
				return err
			})
		case "r":
			p.R, p.RP, err = d.raw()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
}

func TestUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want probe
	}{
		{name: "string", in: `{"s":"x"}`, want: probe{S: "x"}},
		{name: "true", in: `{"b":true}`, want: probe{B: true}},
		{name: "false", in: `{"b":false}`, want: probe{}},
		{name: "number as written", in: `{"n":-1.50}`, want: probe{N: "-1.50"}},
		{name: "nested object", in: `{"o":{"s":"y"}}`, want: probe{O: &probe{S: "y"}}},
		{name: "empty items", in: `{"i":[]}`, want: probe{I: []string{}}},
		{name: "raw", in: `{"r":{"x":[1, 2]}}`, want: probe{R: jsontext.Value(`{"x":[1, 2]}`), RP: "/r"}},
		{name: "raw null", in: `{"r":null}`, want: probe{R: jsontext.Value(`null`), RP: "/r"}},
		{
			name: "any key order and spaces",
			in:   " \n" + `{"r": [true], "i": ["q"], "a": ["p", "q"], "o": {"n": 0}, "n": 1e2, "b": true, "s": "x"}` + "\t\n",
			want: probe{
				S: "x", B: true, N: "1e2", O: &probe{N: "0"},
				A: []string{"p", "q"}, I: []string{"q"}, R: jsontext.Value(`[true]`), RP: "/r",
			},
		},
		{name: "escape", in: `{"s":"\u0041"}`, want: probe{S: "A"}},
		{name: "surrogate pair", in: `{"s":"\ud83d\ude00"}`, want: probe{S: "\U0001F600"}},
		{name: "escaped backslash", in: `{"s":"\\ud800"}`, want: probe{S: `\ud800`}},
		{name: "escaped key", in: `{"\u0073":"x"}`, want: probe{S: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got probe
			if err := unmarshal([]byte(tt.in), "", got.decode); err != nil {
				t.Fatalf("unmarshal(%q) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unmarshal(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecodeJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want probe
		msg  string
	}{
		{name: "replaces the value", in: `{"b":true}`, want: probe{B: true}},
		{name: "decode error", in: `{"s":""}`, want: probe{S: "old"}, msg: "empty string is not allowed"},
		{name: "trailing data", in: `{"s":"x"} {}`, want: probe{S: "old"}, msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := probe{S: "old"}
			assertErrorMsg(t, decodeJSON(&got, []byte(tt.in)), tt.msg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeJSON(%s) left %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestUnmarshalError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		base string
		path string
		msg  string
	}{
		{name: "empty object", in: `{}`, path: "", msg: "empty object is not allowed"},
		{name: "empty nested object", in: `{"o":{}}`, path: "/o", msg: "empty object is not allowed"},
		{name: "empty array", in: `{"a":[]}`, path: "/a", msg: "empty array is not allowed"},
		{name: "empty string", in: `{"s":""}`, path: "/s", msg: "empty string is not allowed"},
		{name: "duplicate key", in: `{"s":"x","s":"y"}`, path: "/s", msg: "duplicate key"},
		{name: "duplicate escaped key", in: `{"s":"x","\u0073":"y"}`, path: "/s", msg: "duplicate key"},
		{name: "key case", in: `{"S":"x"}`, path: "/S", msg: "unknown key"},
		{name: "escaped path", in: `{"a/b":1}`, path: "/a~1b", msg: "unknown key"},
		{name: "object for string", in: `{"s":{}}`, path: "/s", msg: "expected string, got object"},
		{name: "array for string", in: `{"s":[]}`, path: "/s", msg: "expected string, got array"},
		{name: "number for string", in: `{"s":1}`, path: "/s", msg: "expected string, got number"},
		{name: "boolean for string", in: `{"s":true}`, path: "/s", msg: "expected string, got boolean"},
		{name: "null for string", in: `{"s":null}`, path: "/s", msg: "expected string, got null"},
		{name: "string for boolean", in: `{"b":"x"}`, path: "/b", msg: "expected boolean, got string"},
		{name: "string for number", in: `{"n":"1"}`, path: "/n", msg: "expected number, got string"},
		{name: "array for object", in: `{"o":[]}`, path: "/o", msg: "expected object, got array"},
		{name: "object for array", in: `{"a":{"x":1}}`, path: "/a", msg: "expected array, got object"},
		{name: "null", in: `null`, path: "", msg: "expected object, got null"},
		{name: "array element", in: `{"a":["x",1]}`, path: "/a/1", msg: "expected string, got number"},
		{name: "deep", in: `{"o":{"o":{"x":1}}}`, path: "/o/o/x", msg: "unknown key"},
		{name: "invalid UTF-8", in: `{"s":"` + "\xff" + `"}`, path: "/s", msg: "invalid UTF-8"},
		{name: "lone high surrogate", in: `{"s":"\ud800"}`, path: "/s", msg: "surrogate"},
		{name: "lone low surrogate", in: `{"s":"\udc00"}`, path: "/s", msg: "surrogate"},
		{name: "high surrogate before non-surrogate", in: `{"s":"\ud800\u0041"}`, path: "/s", msg: "surrogate"},
		{name: "high surrogate at end", in: `{"s":"a\ud83d"}`, path: "/s", msg: "surrogate"},
		{name: "duplicate key in raw", in: `{"r":{"a":1,"a":2}}`, path: "/r/a", msg: "duplicate key"},
		{name: "second value", in: `{"s":"x"} {}`, path: "", msg: "unexpected data after value"},
		{name: "extra brace", in: `{"s":"x"}}`, path: "", msg: "unexpected data after value"},
		{name: "trailing garbage", in: `{"s":"x"} x`, path: "", msg: "unexpected data after value"},
		{name: "syntax error", in: `{"o":{"s":tru}}`, path: "/o/s", msg: ""},
		{name: "truncated object", in: `{"s":"x"`, path: "", msg: "unexpected end of JSON input"},
		{name: "empty input", in: ``, path: "", msg: "unexpected end of JSON input"},
		{name: "truncated literal", in: `nul`, path: "", msg: "unexpected end of JSON input"},
		{name: "open brace", in: `{`, path: "", msg: "unexpected end of JSON input"},
		{name: "base", in: `{"x":1}`, base: "/r", path: "/r/x", msg: "unknown key"},
		{name: "base nested", in: `{"o":{}}`, base: "/m/0", path: "/m/0/o", msg: "empty object is not allowed"},
		{name: "base syntax error", in: `{"s":"x","s":"y"}`, base: "/m/0", path: "/m/0/s", msg: "duplicate key"},
		{name: "base trailing data", in: `{"s":"x"} {}`, base: "/m/0", path: "/m/0", msg: "unexpected data after value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p probe
			assertError(t, unmarshal([]byte(tt.in), tt.base, p.decode), tt.path, tt.msg)
		})
	}
}
