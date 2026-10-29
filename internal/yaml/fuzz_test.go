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
