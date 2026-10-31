package html

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// collect returns the tokens of z up to the end-of-file token, without it.
func collect(z *tokenizer) []token {
	var toks []token
	for {
		tok := z.next()
		if tok.typ == eofToken {
			return toks
		}
		toks = append(toks, tok)
	}
}

func chars(s string) token { return token{typ: characterToken, data: s} }

func comment(s string) token { return token{typ: commentToken, data: s} }

func startTag(name string, attrs ...Attr) token {
	return token{typ: startTagToken, name: name, attrs: attrs}
}

func endTag(name string) token { return token{typ: endTagToken, name: name} }

func processingInstruction(target, data string) token {
	return token{typ: processingInstructionToken, name: target, data: data}
}

func TestPreprocess(t *testing.T) {
	r := replacement
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"a\r\nb", "a\nb"},
		{"a\rb", "a\nb"},
		{"\r\r\n\n\r", "\n\n\n\n"},
		{"a\n\rb", "a\n\nb"},
		{"a\xffb", "a" + r + "b"},
		{"\xe2\x82a", r + r + "a"},  // a truncated sequence: one U+FFFD for each byte
		{"\xed\xa0\x80", r + r + r}, // a surrogate
		{"\r\xff\r", "\n" + r + "\n"},
		{r, r},
	}
	for _, tt := range tests {
		if got := preprocess(tt.in); got != tt.want {
			t.Errorf("preprocess(%+q) = %+q, want %+q", tt.in, got, tt.want)
		}
	}
	got := collect(newTokenizer("a\r\n<b c='\r\nd\re'>\r<!--\r\n-->"))
	want := []token{chars("a\n"), startTag("b", Attr{Name: "c", Value: "\nd\ne"}), chars("\n"), comment("\n")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokens %+v, want %+v", got, want)
	}
}

func TestSetState(t *testing.T) {
	tests := []struct {
		in    string
		state state // after the first token, a start tag
		want  []token
	}{
		{"<title>a<b>&amp;</TITLE >x", rcdataState,
			[]token{startTag("title"), chars("a<b>&"), endTag("title"), chars("x")}},
		{"<style>a&amp;</style2></style>", rawtextState,
			[]token{startTag("style"), chars("a&amp;</style2>"), endTag("style")}},
		{"<script><!--<script></script>--></script>x", scriptDataState,
			[]token{startTag("script"), chars("<!--<script></script>-->"), endTag("script"), chars("x")}},
		{"<plaintext></plaintext>&amp;", plaintextState, []token{startTag("plaintext"), chars("</plaintext>&amp;")}},
		{"<title>a<b>&amp;</title>", dataState,
			[]token{startTag("title"), chars("a"), startTag("b"), chars("&"), endTag("title")}},
	}
	for _, tt := range tests {
		z := newTokenizer(tt.in)
		first := z.next()
		z.setState(tt.state)
		if got := append([]token{first}, collect(z)...); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q in state %d after the first token: tokens %+v, want %+v", tt.in, tt.state, got, tt.want)
		}
	}
	z := newTokenizer("</title>")
	z.setState(rcdataState)
	if got, want := collect(z), []token{chars("</title>")}; !reflect.DeepEqual(got, want) {
		t.Errorf("an end tag in RCDATA without a start tag: tokens %+v, want %+v", got, want)
	}
}

func TestCDATAHook(t *testing.T) {
	bogus := []token{chars("a"), comment("[CDATA[b]]"), chars("c")}
	tests := []struct {
		name string
		hook func() bool
		want []token
	}{
		{"nil", nil, bogus},
		{"false", func() bool { return false }, bogus},
		{"true", func() bool { return true }, []token{chars("a"), chars("bc")}},
	}
	for _, tt := range tests {
		z := newTokenizer("a<![CDATA[b]]>c")
		returned := 0
		var calls []int
		if tt.hook != nil {
			z.cdataAllowed = func() bool {
				calls = append(calls, returned)
				return tt.hook()
			}
		}
		var got []token
		for tok := z.next(); tok.typ != eofToken; tok = z.next() {
			got = append(got, tok)
			returned++
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("hook %s: tokens %+v, want %+v", tt.name, got, tt.want)
		}
		// The characters before "<![CDATA[" are returned first: they can change the adjusted current node.
		if tt.hook != nil && !reflect.DeepEqual(calls, []int{1}) {
			t.Errorf("hook %s: called after %v tokens were returned, want once after 1", tt.name, calls)
		}
	}
}

func TestProcessingInstructions(t *testing.T) {
	tests := []struct {
		in   string
		want []token
	}{
		{`<?xml version="1.0"?>`, []token{comment(`?xml version="1.0"?`)}},
		{`<?XmL-StyleSheet href="a.css"?>`, []token{comment(`?XmL-StyleSheet href="a.css"?`)}},
		{"<?xml>", []token{comment("?xml")}},
		{"<?xml-foo bar?>", []token{processingInstruction("xml-foo", "bar")}},
		{"<?xmlfoo?>", []token{processingInstruction("xmlfoo", "")}},
		{"<?Foo_1-2 a?b ?>", []token{processingInstruction("Foo_1-2", "a?b ")}},
		{"<?a  ?x>", []token{processingInstruction("a", "?x")}},
		{"<?a\tb\n?>c", []token{processingInstruction("a", "b\n"), chars("c")}},
		{"<?a b\x00c?>", []token{processingInstruction("a", "b\x00c")}},
		{"<?a\x00?>", []token{comment("?a" + replacement + "?")}},
		{"<?1a?>", []token{comment("?1a?")}},
		{"x<?a b", []token{chars("x")}},
	}
	for _, tt := range tests {
		if got := collect(newTokenizer(tt.in)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%+q: tokens %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestAttributes(t *testing.T) {
	var many strings.Builder
	var manyAttrs []Attr
	many.WriteString("<a")
	for i := range 100 {
		fmt.Fprintf(&many, " a%d=%d", i, i)
		manyAttrs = append(manyAttrs, Attr{Name: "a" + strconv.Itoa(i), Value: strconv.Itoa(i)})
	}
	many.WriteString(" a5=x A99=y z>")
	manyAttrs = append(manyAttrs, Attr{Name: "z"})

	aUml, aUmlLower := string(rune(0xC4)), string(rune(0xE4))
	tests := []struct {
		in   string
		want []token
	}{
		{`<a x=1 X=2 x="&amp;3" y=4>`, []token{startTag("a", Attr{Name: "x", Value: "1"}, Attr{Name: "y", Value: "4"})}},
		{"<a z=1 b=2 m=3>", []token{startTag("a", Attr{Name: "z", Value: "1"}, Attr{Name: "b", Value: "2"},
			Attr{Name: "m", Value: "3"})}},
		{many.String(), []token{startTag("a", manyAttrs...)}},
		{"<A" + aUml + " B" + aUml + "=" + aUml + aUmlLower + ">",
			[]token{startTag("a"+aUml, Attr{Name: "b" + aUml, Value: aUml + aUmlLower})}},
	}
	for _, tt := range tests {
		if got := collect(newTokenizer(tt.in)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%+q: tokens %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestNextAfterEOF(t *testing.T) {
	z := newTokenizer("a<!--b")
	want := []token{chars("a"), comment("b"), {typ: eofToken}, {typ: eofToken}}
	for i, w := range want {
		if got := z.next(); !reflect.DeepEqual(got, w) {
			t.Errorf("token %d: %+v, want %+v", i, got, w)
		}
	}
}

// TestTokenizerLinear tokenizes inputs of about 1 MiB that would take minutes if the tokenizer were quadratic in the
// length of a token or of a run of characters.
func TestTokenizerLinear(t *testing.T) {
	const n = 1 << 20
	long := strings.Repeat("a", n)
	repeat := func(s string) string { return strings.Repeat(s, n/len(s)) }
	// 65536 attributes and as many repeated names: a check of each name against the earlier ones would take seconds.
	var attrs strings.Builder
	attrs.WriteString("<a")
	for i := range 1 << 16 {
		fmt.Fprintf(&attrs, " a%d", i)
	}
	attrs.WriteString(strings.Repeat(" a1", 1<<16) + ">")
	tests := []struct {
		name  string
		in    string
		state state
		size  int // the bytes of names, values and data in the tokens
	}{
		{"text", long, dataState, n},
		{"double-quoted attribute value", `<a b="` + long + `">`, dataState, n + 2},
		{"single-quoted attribute value", `<a b='` + long + `'>`, dataState, n + 2},
		{"unquoted attribute value", "<a b=" + long + ">", dataState, n + 2},
		{"tag name", "<" + long + ">", dataState, n},
		{"attribute name", "<a " + long + ">", dataState, n + 1},
		{"comment", "<!--" + long + "-->", dataState, n},
		{"bogus comment", "<!" + long + ">", dataState, n},
		{"DOCTYPE", "<!DOCTYPE " + long + ` PUBLIC "` + long + `" "` + long + `">`, dataState, 3 * n},
		{"processing instruction", "<?a " + long + "?>", dataState, n + 1},
		{"character references", repeat("&amp;"), dataState, n / 5},
		{"ampersands", repeat("&"), dataState, n},
		{"unknown character reference", "&" + long, dataState, n + 1},
		{"numeric character reference", "&#" + strings.Repeat("9", n) + ";", dataState, len(replacement)},
		{"RCDATA", repeat("</a"), rcdataState, n - n%3},
		{"RAWTEXT", repeat("</a"), rawtextState, n - n%3},
		{"script data", repeat("<!--<script>-"), scriptDataState, n - n%13},
		{"PLAINTEXT", long, plaintextState, n},
		{"CDATA section", repeat("]"), cdataSectionState, n},
		{"attributes", attrs.String(), dataState, -1},
		{"repeated attribute names", "<a" + repeat(" b") + ">", dataState, 2},
	}
	for _, tt := range tests {
		start := time.Now()
		z := newTokenizer(tt.in)
		z.setState(tt.state)
		size := 0
		for _, tok := range collect(z) {
			size += len(tok.name) + len(tok.data) + len(tok.publicID) + len(tok.systemID)
			for _, a := range tok.attrs {
				size += len(a.Name) + len(a.Value)
			}
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: %v for %d bytes", tt.name, d, len(tt.in))
		}
		if tt.size >= 0 && size != tt.size {
			t.Errorf("%s: %d bytes of names, values and data, want %d", tt.name, size, tt.size)
		}
	}
}
