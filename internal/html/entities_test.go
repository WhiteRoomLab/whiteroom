package html

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEntities(t *testing.T) {
	tests := []struct{ name, want string }{
		{"amp;", "&"},
		{"amp", "&"},
		{"quot;", `"`},
		{"Tab;", "\t"},
		{"NotEqualTilde;", string(rune(0x2242)) + string(rune(0x0338))},
		{"Afr;", string(rune(0x1D504))},
		{"CounterClockwiseContourIntegral;", string(rune(0x2233))},
	}
	for _, tt := range tests {
		if got, ok := entities[tt.name]; !ok || got != tt.want {
			t.Errorf("entities[%q] = %+q, %v; want %+q", tt.name, got, ok, tt.want)
		}
	}
	notAlnum := func(r rune) bool { return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') }
	longest, legacy := 0, 0
	for name, s := range entities {
		base, semicolon := strings.CutSuffix(name, ";")
		if base == "" || strings.ContainsFunc(base, notAlnum) {
			t.Errorf("name %q is not ASCII letters and digits with an optional final ';'", name)
		}
		if !semicolon {
			legacy++
			if s2, ok := entities[name+";"]; !ok || s2 != s {
				t.Errorf("entities[%q] = %+q, but entities[%q] = %+q, %v", name, s, name+";", s2, ok)
			}
		}
		if s == "" || !utf8.ValidString(s) {
			t.Errorf("entities[%q] = %+q, want valid UTF-8", name, s)
		}
		longest = max(longest, len(name))
	}
	// entities.json has 2231 names, 106 of them without ';'.
	if len(entities) != 2231 || legacy != 106 {
		t.Errorf("%d names, %d without ';'; want 2231 and 106", len(entities), legacy)
	}
	if longest != maxEntityLen {
		t.Errorf("the longest name has %d bytes, but maxEntityLen = %d", longest, maxEntityLen)
	}
}
