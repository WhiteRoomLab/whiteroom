package phpserialize

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
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

// countLF returns the number of strings in v, keys included, that have an LF.
func countLF(v Value) int {
	n := 0
	if v.Kind == String && strings.Contains(v.Str, "\n") {
		n++
	}
	for _, e := range v.Array {
		n += countLF(e.Key) + countLF(e.Value)
	}
	return n
}

// builtValue builds from the bytes of data a flat array, or a single string, and returns it with the number of its
// strings that have an LF, which is at most 3. The strings have no quote, semicolon or CR, so that a quote followed by
// a semicolon is only the end of a string. The keys are different.
func builtValue(data string) (Value, int, bool) {
	if data == "" {
		return Value{}, 0, false
	}
	mode, rest := data[0], []byte(data[1:])
	withLF := 0
	text := func(n int) string {
		b := slices.Clone(rest[:min(n, len(rest))])
		rest = rest[len(b):]
		hasLF := false
		for i, c := range b {
			switch {
			case c == '"' || c == ';' || c == '\r':
				b[i] = 'x'
			case c == '\n' && withLF == 3:
				b[i] = ' '
			case c == '\n':
				hasLF = true
			}
		}
		if hasLF {
			withLF++
		}
		return string(b)
	}
	if mode&1 == 0 {
		return str(text(32)), withLF, true
	}
	var entries []Entry
	for i := 0; len(rest) > 0 && i < 64; i++ {
		h := rest[0]
		rest = rest[1:]
		key := integer(int64(i))
		if h&1 != 0 {
			key = str(fmt.Sprintf("k%d_", i) + text(int(h>>5)))
		}
		val := integer(int64(h) - 128)
		if h&2 != 0 {
			val = str(text(int(h>>2) & 15))
		}
		entries = append(entries, Entry{Key: key, Value: val})
	}
	return array(entries...), withLF, true
}

func FuzzUnserializeCRLF(f *testing.F) {
	for _, tt := range unserializeTests {
		f.Add(tt.in)
	}
	for _, tt := range unserializeErrorTests {
		f.Add(tt.in)
	}
	for _, tt := range unserializeCRLFTests {
		f.Add(tt.in)
	}
	for _, tt := range unserializeCRLFErrorTests {
		f.Add(tt.in)
	}
	f.Add(serializeCRLF(list(mail(body26), mail(body26), mail(mailBody))))
	f.Add("\x01a\n\x03bc\n\x07d\n\xff\x05ef")
	f.Fuzz(func(t *testing.T, data string) {
		v, n, err := UnserializeCRLF(data)
		sv, serr := Unserialize(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("UnserializeCRLF(%q) = %v (%T), want *Error", data, err, err)
			}
			if e.Offset < 0 || e.Offset > len(data) {
				t.Errorf("UnserializeCRLF(%q) = %v, outside the input", data, err)
			}
			if v.Kind != Null || !wellFormed(v) || n != 0 {
				t.Errorf("UnserializeCRLF(%q) = %+v, %d with an error, want the zero Value and 0", data, v, n)
			}
			if serr == nil {
				t.Errorf("Unserialize(%q) succeeded, but UnserializeCRLF = %v", data, err)
			}
		} else {
			if len(data) > MaxBytes {
				t.Fatalf("UnserializeCRLF(%d bytes) succeeded, want an error above %d", len(data), MaxBytes)
			}
			if !wellFormed(v) {
				t.Fatalf("UnserializeCRLF(%q) = %+v, want only the field for Kind set", data, v)
			}
			if d := depth(v); d > MaxDepth {
				t.Errorf("UnserializeCRLF(%q) has %d levels of arrays, want at most %d", data, d, MaxDepth)
			}
			if lf := countLF(v); n < 0 || n > lf {
				t.Errorf("UnserializeCRLF(%q) = %s, %d; want at most %d, the strings that have an LF", data, serialize(v), n, lf)
			}
			switch {
			case serr == nil && (n != 0 || !equalValue(v, sv)):
				t.Errorf("UnserializeCRLF(%q) = %s, %d; want what Unserialize reads, %s, and 0", data, serialize(v), n, serialize(sv))
			case serr != nil && n == 0:
				t.Errorf("UnserializeCRLF(%q) = %s, 0, but Unserialize fails: %v", data, serialize(v), serr)
			}
		}

		built, lf, ok := builtValue(data)
		if !ok {
			return
		}
		text := serializeCRLF(built)
		got, n, err := UnserializeCRLF(text)
		if err != nil || n != lf || !equalValue(got, built) {
			t.Errorf("UnserializeCRLF(%q) = %s, %d, %v; want %s, %d", text, serialize(got), n, err, serialize(built), lf)
		}
	})
}
