package yaml

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkPointers checks that positions has the JSON Pointers of all values in out, and no others.
func checkPointers(t *testing.T, out []byte, positions map[string]Position) {
	t.Helper()
	dec := jsontext.NewDecoder(bytes.NewBuffer(out))
	want := make(map[string]bool)
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("ReadToken() of %s: %v", out, err)
		}
		if k := tok.Kind(); k == jsontext.KindEndObject || k == jsontext.KindEndArray {
			continue
		}
		// An object with an odd length has just read a name.
		if k, n := dec.StackIndex(dec.StackDepth()); k == jsontext.KindBeginObject && n%2 == 1 {
			continue
		}
		want[string(dec.StackPointer())] = true
	}
	for ptr := range want {
		if _, ok := positions[ptr]; !ok {
			t.Errorf("positions has no %q", ptr)
		}
	}
	for ptr := range positions {
		if !want[ptr] {
			t.Errorf("positions has %q, which is not in %s", ptr, out)
		}
	}
}

func FuzzToJSON(f *testing.F) {
	for _, tt := range toJSONTests {
		f.Add([]byte(tt.in))
	}
	f.Add([]byte(worksSchema))
	for _, tt := range readSuite(f) {
		f.Add([]byte(tt.YAML))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		out, positions, err := ToJSON(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("ToJSON(%q) = %v (%T), want *Error", data, err, err)
			}
			if out != nil || positions != nil {
				t.Errorf("ToJSON(%q) = %s, %v with an error", data, out, positions)
			}
			lines := strings.Split(strings.TrimPrefix(string(data), "\xef\xbb\xbf"), "\n")
			if e.Line < 1 || e.Line > len(lines) || e.Column < 1 || e.Column > utf8.RuneCountInString(lines[e.Line-1])+1 {
				t.Errorf("ToJSON(%q) = %v, outside the input", data, err)
			}
			return
		}
		if !jsontext.Value(out).IsValid() {
			t.Fatalf("ToJSON(%q) = %s, not valid JSON", data, out)
		}
		checkPointers(t, out, positions)
		again, _, err := ToJSON(out)
		if err != nil || !bytes.Equal(again, out) {
			t.Errorf("ToJSON(%s) = %s, %v; want the same JSON", out, again, err)
		}
	})
}

// jsonFacts are the things in a JSON value that make FromJSON fail.
type jsonFacts struct {
	depth    int  // the deepest nesting of objects and lists
	null     bool // there is a null
	badChar  bool // a string has a character that YAML cannot have
	longName bool // a name has so many characters that it may be longer than ToJSON reads once written
}

// scanJSON reads data, a valid JSON value.
func scanJSON(t testing.TB, data []byte) jsonFacts {
	t.Helper()
	var facts jsonFacts
	dec := jsontext.NewDecoder(bytes.NewBuffer(data))
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			return facts
		}
		if err != nil {
			t.Fatalf("ReadToken() of %q: %v", data, err)
		}
		facts.depth = max(facts.depth, dec.StackDepth())
		switch tok.Kind() {
		case jsontext.KindNull:
			facts.null = true
		case jsontext.KindString:
			s := tok.String()
			for _, r := range s {
				if r == 0x7F || 0x80 <= r && r <= 0x9F && r != 0x85 || r == 0xFEFF || r == 0xFFFE || r == 0xFFFF {
					facts.badChar = true
				}
			}
			// An object with an odd length has just read a name.
			if k, n := dec.StackIndex(dec.StackDepth()); k == jsontext.KindBeginObject && n%2 == 1 && utf8.RuneCountInString(s) >= 256 {
				facts.longName = true
			}
		case jsontext.KindFalse, jsontext.KindTrue, jsontext.KindNumber,
			jsontext.KindBeginObject, jsontext.KindEndObject, jsontext.KindBeginArray, jsontext.KindEndArray, jsontext.KindInvalid:
		}
	}
}

func FuzzFromJSON(f *testing.F) {
	for _, tt := range toJSONTests {
		if out, _, err := ToJSON([]byte(tt.in)); err == nil {
			f.Add(out)
		}
	}
	if out, _, err := ToJSON([]byte(worksSchema)); err == nil {
		f.Add(out)
	}
	for _, s := range []string{
		`{}`,
		`{"a":null}`,
		`[{"a":1}]`,
		`{"a":[1,"b",{"c":[]},[[]],{}],"d":{"e":-0.5e+10}}`,
		`{"":"","true":"true","a b":"a: b","x":["- x","[y]","#z","0x1F",",",""],"y":{"z":{"1":[[{"k":"v"}]]}}}`,
		`{"k":` + quoteJSON("a"+string(rune(0x85))+"b\t"+string(rune(0))+string(rune(0x2028))) + `}`,
		`{"k":` + quoteJSON("a"+string(rune(0x7F))) + `}`,
		`{"` + jsonEscape('a') + `":"` + jsonEscape('b') + `\/"}`,
		`{"a":` + strings.Repeat("[", 99) + "1" + strings.Repeat("]", 99) + "}",
		`{"a":` + strings.Repeat(`{"a":`, 100) + "1" + strings.Repeat("}", 100) + "}",
		`{"` + strings.Repeat("k", 1025) + `":1}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := FromJSON(data)
		v := jsontext.Value(data)
		if !v.IsValid() || v.Kind() != jsontext.KindBeginObject {
			if err == nil || out != nil {
				t.Fatalf("FromJSON(%q) = %q, %v; want an error", data, out, err)
			}
			return
		}
		facts := scanJSON(t, data)
		switch mustFail := facts.null || facts.depth > maxDepth || facts.badChar; {
		case err != nil && out != nil:
			t.Fatalf("FromJSON(%q) = %q, %v", data, out, err)
		case err == nil && mustFail:
			t.Fatalf("FromJSON(%q) = %q; want an error for %+v", data, out, facts)
		case err != nil && (strings.Contains(err.Error(), "does not read back") || !mustFail && !facts.longName):
			t.Fatalf("FromJSON(%q) = %v for %+v", data, err, facts)
		case err != nil:
			return
		}
		checkYAML(t, string(data), out)
	})
}
