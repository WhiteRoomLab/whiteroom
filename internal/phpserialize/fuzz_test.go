package phpserialize

import (
	"errors"
	"strconv"
	"testing"
)

func FuzzUnserialize(f *testing.F) {
	for _, tt := range unserializeTests {
		f.Add(tt.in)
	}
	for _, tt := range unserializeErrorTests {
		f.Add(tt.in)
	}
	f.Fuzz(func(t *testing.T, data string) {
		v, err := Unserialize(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Unserialize(%q) = %v (%T), want *Error", data, err, err)
			}
			if e.Offset < 0 || e.Offset > len(data) {
				t.Errorf("Unserialize(%q) = %v, outside the input", data, err)
			}
			if v.Kind != Null || !wellFormed(v) {
				t.Errorf("Unserialize(%q) = %+v with an error, want the zero Value", data, v)
			}
			return
		}
		if len(data) > MaxBytes {
			t.Fatalf("Unserialize(%d bytes) succeeded, want an error above %d", len(data), MaxBytes)
		}
		if !wellFormed(v) {
			t.Fatalf("Unserialize(%q) = %+v, want only the field for Kind set", data, v)
		}
		if d := depth(v); d > MaxDepth {
			t.Errorf("Unserialize(%q) has %d levels of arrays, want at most %d", data, d, MaxDepth)
		}
		again, err := Unserialize(serialize(v))
		if err != nil || !equalValue(v, again) {
			t.Errorf("Unserialize(%q) = %s, but reading it again gave %s, %v", data, serialize(v), serialize(again), err)
		}
		// WordPress does not take d: for serialized when it has INF, NAN, or a lowercase e.
		if v.Kind != Float && !LooksSerialized(data) {
			t.Errorf("Unserialize(%q) succeeded, but LooksSerialized is false", data)
		}
		if v.Kind == Array && len(v.Array) <= 64 {
			for _, e := range v.Array {
				key := e.Key.Str
				if e.Key.Kind == Int {
					key = strconv.FormatInt(e.Key.Int, 10)
				}
				if got, ok := v.Lookup(key); !ok || !equalValue(got, e.Value) {
					t.Errorf("Lookup(%q) of %s = %s, %v; want %s", key, serialize(v), serialize(got), ok, serialize(e.Value))
				}
			}
		}
	})
}
