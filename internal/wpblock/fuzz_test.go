package wpblock

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	soupSpaces = []string{" ", " ", " ", "\t", "\n", "\r", "\f", "  ", "\v", ""}
	soupNames  = []string{
		"a", "a", "b", "c", "core/c", "my/box", "x-1_y", "a1", "A", "1a", "a/b/c", "a/", "-a", "core/a", "paragraph", "",
	}
	soupAttrs = []string{
		`{}`, `{"a":1}`, `{"a":"b","c":[1,2,{"d":null}]}`, `{"a":"}"}`, `{"a":"} -->"}`, `{"a":"-->"}`, `{"a":{"b":{"c":1}}}`,
		`{"a":1,"a":2}`, `{"0":"x","1":"y"}`, `{"1":"x","0":"y"}`, `{"a":}`, `{"a":1,}`, `{a:1}`, `{'a':1}`,
		`{"a":"\` + `ud800"}`, `{"a":"\` + `ud83d\` + `ude00"}`, `{"a":"\` + `udc00"}`, `{"a":1.5,"b":-2,"c":1e2,"d":1E-2,"e":0.25}`,
		`{"a":true,"b":false,"c":null}`, `{"a":TRUE}`, `{"a":01}`, `{"a":"x"} {"b":1}`, `{"":1}`, `{"a":[]}`, `{"a":[[],[[]]]}`,
		`{"a":"\n\t"}`, `{"a":"\` + `u0041\` + `u00e9"}`, `{"x":{"y":{"z":{}}}}`, `{"a":1}}`, `{{"a":1}`, `{"a":"{"}`, `{"a":1} }`,
		`{"0":{"0":1}}`, `{"1":1}`, `{"a":-0}`, `{"a":0.0}`, `{"a":[1,2,3],"a":4}`, `{"b":2,"a":1,"b":3}`,
	}
	soupHTML = []string{
		"x", "abc", "<p>a</p>", "\n", " ", "\t", "\v", "\f", "0", "00", "<!--", "-->", "<!-- note -->", "}", "{", "} -->", "<!-- /",
		"<!--wp:a -->", "<!-- wp:", "wp:a", "/", "<!", "--", "\r\n", "<br>", "a b c", "<!-- wp:a {\"x\":1 -->", "{\"a\":1}",
		string(rune(0xe9)) + string(rune(0x20ac)), "\x00",
	}
)

// treePieces are what mutate puts into a document that genItems wrote.
var treePieces = []string{"<!-- wp:a -->", "<!-- /wp:a -->", "<!-- wp:b /-->", "{", "}", "-->", " ", "/", "\n", "\f", "\v", `{"a":1} `, "\x00"}

func soupPick(r *rand.Rand, xs []string) string { return xs[r.IntN(len(xs))] }

// soupDelimiter returns a delimiter that is whole, or has been spoiled a little.
func soupDelimiter(r *rand.Rand) string {
	var sb strings.Builder
	sb.WriteString("<!--")
	sb.WriteString(soupPick(r, soupSpaces))
	kind := r.IntN(10)
	if kind >= 6 && kind < 8 {
		sb.WriteByte('/')
	}
	sb.WriteString("wp:")
	sb.WriteString(soupPick(r, soupNames))
	sb.WriteString(soupPick(r, soupSpaces))
	if r.IntN(3) == 0 {
		sb.WriteString(soupPick(r, soupAttrs))
		sb.WriteString(soupPick(r, soupSpaces))
	}
	if kind >= 8 || r.IntN(40) == 0 {
		sb.WriteByte('/')
	}
	sb.WriteString("-->")
	return sb.String()
}

func soupDocument(r *rand.Rand) string {
	var sb strings.Builder
	for range 1 + r.IntN(14) {
		if r.IntN(10) < 6 {
			sb.WriteString(soupDelimiter(r))
		} else {
			sb.WriteString(soupPick(r, soupHTML))
		}
	}
	return sb.String()
}

// mutate changes a document a little, with bytes and pieces that could spoil a delimiter.
func mutate(r *rand.Rand, doc string, pieces []string) string {
	for range r.IntN(4) {
		if doc == "" {
			break
		}
		i := r.IntN(len(doc))
		switch r.IntN(5) {
		case 0:
			doc = doc[:i] + doc[min(len(doc), i+1+r.IntN(8)):]
		case 1:
			j := min(len(doc), i+1+r.IntN(30))
			doc = doc[:j] + doc[i:j] + doc[j:]
		case 2:
			doc = doc[:i] + soupPick(r, pieces) + doc[i:]
		case 3:
			doc = doc[:i] + soupPick(r, pieces)[:1] + doc[i+1:]
		case 4:
			doc = doc[:i]
		}
	}
	return doc
}

// A refToken is a delimiter that either of the two ways of reading found.
type refToken struct {
	kind       tokenKind
	name       string
	start, end int
	attrs      string // as jsonText writes them, and empty for a closing delimiter
}

func scanTokens(doc string) []refToken {
	s := scanner{doc: doc}
	var toks []refToken
	for off := 0; ; {
		tok, ok := s.next(off)
		if !ok {
			return toks
		}
		rt := refToken{kind: tok.kind, name: tok.name, start: tok.start, end: tok.end}
		if tok.kind != kindCloser {
			rt.attrs = jsonText(tok.attrs)
		}
		toks = append(toks, rt)
		off = tok.end
	}
}

// delimiterRE finds a delimiter in the way that the scanner does not: the attributes are the shortest run that is
// followed by a "}", white space, and the end.
var delimiterRE = regexp.MustCompile(`(?s)<!--[ \t\f\r\n]+(/)?wp:([a-z][a-z0-9_-]*/)?([a-z][a-z0-9_-]*)[ \t\f\r\n]+(\{.*?\}[ \t\f\r\n]+)?(/)?-->`)

func regexpTokens(doc string) []refToken {
	var toks []refToken
	for off := 0; off <= len(doc); {
		m := delimiterRE.FindStringSubmatchIndex(doc[off:])
		if m == nil {
			return toks
		}
		text := func(group int) (string, bool) {
			if m[2*group] < 0 {
				return "", false
			}
			return doc[off+m[2*group] : off+m[2*group+1]], true
		}
		namespace, ok := text(2)
		if !ok {
			namespace = "core/"
		}
		word, _ := text(3)
		rt := refToken{name: namespace + word, start: off + m[0], end: off + m[1]}
		_, closer := text(1)
		_, void := text(5)
		switch {
		case void:
			rt.kind = kindVoid
		case closer:
			rt.kind = kindCloser
		default:
			rt.kind = kindOpener
		}
		if rt.kind != kindCloser {
			rt.attrs = "{}"
			if attrs, ok := text(4); ok {
				v, _ := DecodeAttrs(attrs)
				rt.attrs = jsonText(v)
			}
		}
		toks = append(toks, rt)
		off = rt.end
	}
	return toks
}

func tokensText(toks []refToken) string {
	var sb strings.Builder
	for _, tk := range toks {
		fmt.Fprintf(&sb, "  kind %d %s %d-%d %s\n", tk.kind, tk.name, tk.start, tk.end, tk.attrs)
	}
	return sb.String()
}

func checkTokens(t testing.TB, doc string) {
	t.Helper()
	got, want := scanTokens(doc), regexpTokens(doc)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the delimiters of %q are\n%swant\n%s", doc, tokensText(got), tokensText(want))
	}
}

func TestScannerAgainstRegexp(t *testing.T) {
	for _, tt := range parseTests {
		checkTokens(t, tt.doc)
	}
	r := rand.New(rand.NewPCG(52, 5))
	for range 3000 {
		checkTokens(t, soupDocument(r))
		var sb strings.Builder
		writeItems(&sb, genItems(r, 0))
		checkTokens(t, mutate(r, sb.String(), treePieces))
	}
	// Longer documents, in which the scanner searches for a "}" that it has found or not found before.
	for range 300 {
		var sb strings.Builder
		for range 20 + r.IntN(60) {
			if r.IntN(3) == 0 {
				sb.WriteString(soupDelimiter(r))
			} else {
				sb.WriteString(soupPick(r, soupHTML))
			}
			if r.IntN(4) == 0 {
				sb.WriteString(soupPick(r, soupAttrs))
			}
		}
		checkTokens(t, sb.String())
	}
}

// namedBlocks returns the number of blocks that have a name, inner blocks included.
func namedBlocks(bs []Block) int {
	n := 0
	for i := range bs {
		if bs[i].Name != "" {
			n++
		}
		n += namedBlocks(bs[i].Inner)
	}
	return n
}

func FuzzParse(f *testing.F) {
	for _, tt := range parseTests {
		f.Add(tt.doc)
	}
	for _, s := range []string{"<!-- wp:a {", "<!-- /wp:a -->", "<!-- wp:a /-->", "} -->", `{"a":"} -->"} -->`, "<!-- wp:a -->", "<!--", ""} {
		f.Add(s)
	}
	const small = 64
	f.Fuzz(func(t *testing.T, doc string) {
		p := &parser{doc: doc, s: scanner{doc: doc}}
		got, err := p.run(Options{})
		if err != nil {
			t.Fatalf("Parse(%q) = %v", doc, err)
		}
		if msg := problem(doc, got); msg != "" {
			t.Fatalf("Parse(%q): %s", doc, msg)
		}
		if limit := 8*len(doc) + 1<<16; p.s.scanned > limit {
			t.Errorf("Parse(%q) looked at %d bytes, want at most %d", doc, p.s.scanned, limit)
		}
		n := namedBlocks(got)
		again, err := Parse(doc, Options{MaxBlocks: small})
		switch {
		case n > small && (!errors.Is(err, ErrLimit) || again != nil):
			t.Errorf("Parse(%q) with %d blocks, and a limit of %d = %d blocks, %v; want ErrLimit", doc, n, small, len(again), err)
		case n <= small && (err != nil || !reflect.DeepEqual(again, got)):
			t.Errorf("Parse(%q) with %d blocks, and a limit of %d = %d blocks, %v; want the same blocks", doc, n, small, len(again), err)
		}
		checkTokens(t, doc)
	})
}

// wellFormed reports whether only the fields for Kind are set in v and the values in it, and the names in an object
// are not repeated.
func wellFormed(v Value, depth int) bool {
	if depth > MaxAttrsDepth {
		return false
	}
	switch v.Kind {
	case Null:
		return !v.Bool && v.Number == "" && v.String == "" && v.Array == nil && v.Object == nil
	case Bool:
		return v.Number == "" && v.String == "" && v.Array == nil && v.Object == nil
	case Number:
		return v.Number != "" && !v.Bool && v.String == "" && v.Array == nil && v.Object == nil
	case String:
		return !v.Bool && v.Number == "" && v.Array == nil && v.Object == nil
	case Array:
		if v.Bool || v.Number != "" || v.String != "" || v.Object != nil {
			return false
		}
		for _, e := range v.Array {
			if !wellFormed(e, depth+1) {
				return false
			}
		}
	case Object:
		if v.Bool || v.Number != "" || v.String != "" || v.Array != nil {
			return false
		}
		names := map[string]bool{}
		for _, m := range v.Object {
			if names[m.Name] || !wellFormed(m.Value, depth+1) {
				return false
			}
			names[m.Name] = true
		}
	}
	return true
}

// plainValue is v as encoding/json/v2 reads it into an any, and whether it can: v2 does not read a number that a
// float64 cannot hold.
func plainValue(v Value) (any, bool) {
	switch v.Kind {
	case Null:
		return nil, true
	case Bool:
		return v.Bool, true
	case Number:
		f, err := strconv.ParseFloat(v.Number, 64)
		return f, err == nil
	case String:
		return v.String, true
	case Array:
		list := make([]any, 0, len(v.Array))
		for _, e := range v.Array {
			x, ok := plainValue(e)
			if !ok {
				return nil, false
			}
			list = append(list, x)
		}
		return list, true
	case Object:
		m := make(map[string]any, len(v.Object))
		for _, e := range v.Object {
			x, ok := plainValue(e.Value)
			if !ok {
				return nil, false
			}
			m[e.Name] = x
		}
		return m, true
	}
	return nil, false
}

func plainDepth(x any) int {
	deepest := 0
	switch x := x.(type) {
	case []any:
		for _, e := range x {
			deepest = max(deepest, plainDepth(e))
		}
		return deepest + 1
	case map[string]any:
		for _, e := range x {
			deepest = max(deepest, plainDepth(e))
		}
		return deepest + 1
	}
	return 0
}

// plainRead reads s with encoding/json/v2. valid is whether s is JSON, and tree is what v2 makes of it as an any, which it
// does not when a name is repeated (v2 would merge the values) or a number is out of range.
func plainRead(s string) (tree any, hasTree, valid bool) {
	var raw jsontext.Value
	if json.Unmarshal([]byte(s), &raw, jsontext.AllowDuplicateNames(true)) != nil {
		return nil, false, false
	}
	return tree, json.Unmarshal([]byte(s), &tree) == nil, true
}

func checkAgainstJSON(t testing.TB, s string) {
	t.Helper()
	tree, hasTree, valid := plainRead(s)
	v, ok := DecodeAttrs(s)
	if !ok {
		if v != nil {
			t.Fatalf("DecodeAttrs(%q) = %s with false", s, jsonText(v))
		}
		// Only PHP's limit on the depth is a reason to refuse what v2 reads.
		if hasTree && plainDepth(tree) <= MaxAttrsDepth {
			t.Errorf("DecodeAttrs(%q) is invalid, but encoding/json/v2 reads it with %d levels", s, plainDepth(tree))
		}
		return
	}
	if v == nil || !wellFormed(*v, 1) {
		t.Fatalf("DecodeAttrs(%q) = %+v, want a value that has only the fields for its kind", s, v)
	}
	if !valid {
		t.Errorf("DecodeAttrs(%q) = %s, but encoding/json/v2 says it is not JSON", s, jsonText(v))
	}
	again, ok := DecodeAttrs(jsonText(v))
	if !ok || !reflect.DeepEqual(again, v) {
		t.Errorf("DecodeAttrs(%q) = %s, but reading that gave %s, %v", s, jsonText(v), jsonText(again), ok)
	}
	if want, readable := plainValue(*v); hasTree && (!readable || !reflect.DeepEqual(tree, want)) {
		t.Errorf("DecodeAttrs(%q) = %s, but encoding/json/v2 reads %v", s, jsonText(v), tree)
	}
}

func FuzzDecodeAttrs(f *testing.F) {
	for _, tt := range decodeTests {
		f.Add(tt.in)
	}
	for _, a := range soupAttrs {
		f.Add(a)
	}
	f.Fuzz(func(t *testing.T, s string) { checkAgainstJSON(t, s) })
}

func TestDecodeAttrsAgainstJSON(t *testing.T) {
	// The seeds of FuzzDecodeAttrs, and changes of them, as a test that runs without fuzzing.
	r := rand.New(rand.NewPCG(52, 6))
	var seeds []string
	for _, tt := range decodeTests {
		seeds = append(seeds, tt.in)
	}
	seeds = append(seeds, soupAttrs...)
	pieces := []string{`"`, `\`, `{`, `}`, `[`, `]`, `,`, `:`, "\x00", "\x1f", " ", "\t", "\f", "\x7f", "\xc3", "0", "-", ".", "e", "t", "n", "1e999"}
	for _, seed := range slices.Clone(seeds) {
		for range 20 {
			seeds = append(seeds, mutate(r, seed, pieces))
		}
	}
	for _, s := range seeds {
		checkAgainstJSON(t, s)
	}
}
