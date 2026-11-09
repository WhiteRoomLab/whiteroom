package wpblock

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// The time that Parse takes grows with the length of the document, and the scanner counts the bytes that it looks at, so
// that a test can say so without a clock.
var scanInputs = []struct {
	name string
	make func(size int) string
}{
	{"attributes that do not end", func(n int) string { return strings.Repeat("<!-- wp:a {", n/11) }},
	{"string that does not end, and braces", func(n int) string { return `<!-- wp:a {"x":"` + strings.Repeat("}", n) }},
	{"braces and spaces", func(n int) string { return "<!-- wp:a {" + strings.Repeat("} ", n/2) }},
	{"braces and long white space", func(n int) string { return "<!-- wp:a {" + strings.Repeat("}"+strings.Repeat(" ", 999), n/1000) }},
	{"braces and slashes", func(n int) string { return "<!-- wp:a {" + strings.Repeat("} /", n/3) }},
	{"braces and half of an end", func(n int) string { return "<!-- wp:a {" + strings.Repeat("} --", n/4) }},
	{"openings of comments", func(n int) string { return strings.Repeat("<!--", n/4) }},
	{"openings and white space", func(n int) string { return strings.Repeat("<!-- ", n/5) }},
	{"openings and names", func(n int) string { return strings.Repeat("<!-- wp:a", n/9) }},
	{"openings and names, and white space", func(n int) string { return strings.Repeat("<!-- wp:a ", n/10) }},
	{"long white space", func(n int) string { return "<!-- " + strings.Repeat(" ", n) }},
	{"long name", func(n int) string { return "<!-- wp:" + strings.Repeat("a", n) }},
	{"long namespace", func(n int) string { return "<!-- wp:" + strings.Repeat("a", n) + "/b" }},
	{"long white space after a name", func(n int) string { return "<!-- wp:a" + strings.Repeat(" ", n) }},
	{"attributes that end at the end", func(n int) string { return strings.Repeat("<!-- wp:a {", n/11) + "} -->" }},
	{"attributes in the attributes", func(n int) string { return "<!-- wp:a {" + strings.Repeat(`"<!-- wp:b {`, n/12) + `} -->` }},
	{"long string in the attributes", func(n int) string { return `<!-- wp:a {"x":"` + strings.Repeat("a", n) + `"} -->` }},
	{"long array in the attributes", func(n int) string { return `<!-- wp:a {"x":[` + strings.Repeat("1,", 50000) + `1]} -->` }},
	{"deep attributes", func(n int) string {
		return "<!-- wp:a " + strings.Repeat(`{"a":`, n/5) + `1` + strings.Repeat("}", n/5) + " -->"
	}},
	{"attributes with many braces, one after another", func(n int) string {
		return strings.Repeat(`<!-- wp:a {"x":"`+strings.Repeat("}", 500)+`"} -->`, n/520)
	}},
	{"attributes that nearly end, one after another", func(n int) string {
		return strings.Repeat(`<!-- wp:a {"x":1}-->`+strings.Repeat(" ", 100), n/120) + "} -->"
	}},
	{"closing delimiter first", func(n int) string { return "<!-- /wp:a -->" + strings.Repeat("<!-- wp:a {", n/11) }},
	{"nested", func(n int) string {
		levels := n / 27
		return strings.Repeat("<!-- wp:a -->", levels) + strings.Repeat("<!-- /wp:a -->", levels)
	}},
	{"nested and open", func(n int) string { return strings.Repeat("<!-- wp:a -->", n/13) }},
	{"nested and open, with HTML", func(n int) string { return strings.Repeat("<!-- wp:a -->x", n/14) }},
	{"void blocks", func(n int) string { return strings.Repeat("<!-- wp:a /-->", n/14) }},
	{"blocks with attributes that have many braces", func(n int) string {
		return strings.Repeat(`<!-- wp:a {"x":"`+strings.Repeat("}", 100)+`"} /-->`, n/120)
	}},
	{"closing delimiters", func(n int) string { return strings.Repeat("<!-- wp:a -->x<!-- /wp:a -->", n/28) }},
}

func TestScanLimits(t *testing.T) {
	for _, tt := range scanInputs {
		for _, size := range []int{1 << 20, 4 << 20} {
			doc := tt.make(size)
			p := &parser{doc: doc, s: scanner{doc: doc}}
			got, err := p.run(Options{MaxBlocks: math.MaxInt})
			if err != nil {
				t.Errorf("%s, %d bytes: Parse = %v", tt.name, len(doc), err)
				continue
			}
			if limit := 8*len(doc) + 1<<16; p.s.scanned > limit {
				t.Errorf("%s, %d bytes: looked at %d bytes, want at most %d", tt.name, len(doc), p.s.scanned, limit)
			}
			if size == 1<<20 {
				if msg := problem(doc, got); msg != "" {
					t.Errorf("%s, %d bytes: %s", tt.name, len(doc), msg)
				}
			}
		}
	}
}

func TestDeepNesting(t *testing.T) {
	const levels = DefaultMaxBlocks
	tests := []struct {
		name     string
		doc      string
		unclosed bool
	}{
		{"closed", strings.Repeat("<!-- wp:a -->", levels) + strings.Repeat("<!-- /wp:a -->", levels), false},
		{"open", strings.Repeat("<!-- wp:a -->x", levels), true},
	}
	for _, tt := range tests {
		got, err := Parse(tt.doc, Options{})
		if err != nil || len(got) != 1 {
			t.Errorf("%s: Parse = %d blocks, %v; want 1", tt.name, len(got), err)
			continue
		}
		depth := 0
		for b := &got[0]; ; b = &b.Inner[0] {
			depth++
			if b.Unclosed != tt.unclosed {
				t.Fatalf("%s: level %d has Unclosed %v", tt.name, depth, b.Unclosed)
			}
			if len(b.Inner) == 0 {
				break
			}
		}
		if depth != levels {
			t.Errorf("%s: %d levels, want %d", tt.name, depth, levels)
		}
		if _, err := Parse(tt.doc, Options{MaxBlocks: levels - 1}); !errors.Is(err, ErrLimit) {
			t.Errorf("%s: with a limit of %d, Parse = %v, want ErrLimit", tt.name, levels-1, err)
		}
	}
}

func TestMaxBlocksStopsTheRead(t *testing.T) {
	doc := strings.Repeat("<!-- wp:a /-->", (4<<20)/14)
	p := &parser{doc: doc, s: scanner{doc: doc}}
	got, err := p.run(Options{MaxBlocks: 1000})
	if !errors.Is(err, ErrLimit) || got != nil {
		t.Fatalf("Parse = %d blocks, %v; want ErrLimit", len(got), err)
	}
	if p.s.scanned > 100000 {
		t.Errorf("looked at %d bytes of %d before it gave up", p.s.scanned, len(doc))
	}
}
