package nfc

import (
	"cmp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// fromHex builds a string from code points written as in NormalizationTest.txt, such as "0065 0301".
func fromHex(t testing.TB, s string) string {
	t.Helper()
	var b strings.Builder
	for _, f := range strings.Fields(s) {
		r, err := strconv.ParseUint(f, 16, 32)
		if err != nil || !utf8.ValidRune(rune(r)) {
			t.Fatalf("bad code point %q in %q", f, s)
		}
		b.WriteRune(rune(r))
	}
	return b.String()
}

func TestUnicodeVersion(t *testing.T) {
	if unicodeVersion != unicode.Version {
		t.Errorf("tables are for Unicode %s, but unicode.Version is %s; run go generate ./internal/nfc", unicodeVersion, unicode.Version)
	}
}

func TestTables(t *testing.T) {
	for i := 1; i < len(props); i++ {
		if props[i-1].r >= props[i].r {
			t.Fatalf("props not in increasing order at U+%04X", props[i].r)
		}
	}
	for i := 1; i < len(compositions); i++ {
		a, b := compositions[i-1], compositions[i]
		if cmp.Or(cmp.Compare(a.first, b.first), cmp.Compare(a.second, b.second)) >= 0 {
			t.Fatalf("compositions not in increasing order at U+%04X U+%04X", b.first, b.second)
		}
	}
	full := func(r rune) string {
		if d := lookup(r).decomp; d != "" {
			return d
		}
		return string(r)
	}
	for _, c := range compositions {
		if got, want := lookup(c.composite).decomp, full(c.first)+full(c.second); got != want {
			t.Errorf("U+%04X: decomp = %+q, want %+q", c.composite, got, want)
		}
	}
	for _, p := range props {
		if p.qc == qcNo && p.decomp == "" {
			t.Errorf("U+%04X: NFC_QC=N without a decomposition", p.r)
		}
	}
	tests := []struct {
		r    rune
		want prop
	}{
		{0x0301, prop{0x0301, 230, qcMaybe, ""}},
		{0x00C0, prop{0x00C0, 0, qcYes, fromHex(t, "0041 0300")}},
		{0x0958, prop{0x0958, 0, qcNo, fromHex(t, "0915 093C")}},
		{'a', prop{qc: qcYes}},
		{0xAC00, prop{qc: qcYes}},
	}
	for _, tt := range tests {
		if got := lookup(tt.r); got != tt.want {
			t.Errorf("lookup(U+%04X) = %+v, want %+v", tt.r, got, tt.want)
		}
	}
}
