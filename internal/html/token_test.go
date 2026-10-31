package html

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// tokenizerSkips counts the tests with a lone surrogate, which a Go string cannot hold (unicodeCharsProblematic.test).
const tokenizerSkips = 4

type tokenizerTest struct {
	Description   string         `json:"description"`
	Input         string         `json:"input"`
	Output        jsontext.Value `json:"output"`
	InitialStates []string       `json:"initialStates"`
	LastStartTag  string         `json:"lastStartTag"`
	DoubleEscaped bool           `json:"doubleEscaped"`
	Errors        jsontext.Value `json:"errors"`
}

var testStates = map[string]state{
	"Data state":          dataState,
	"PLAINTEXT state":     plaintextState,
	"RCDATA state":        rcdataState,
	"RAWTEXT state":       rawtextState,
	"Script data state":   scriptDataState,
	"CDATA section state": cdataSectionState,
}

// TestTokenizerSuite runs the html5lib tokenizer tests (see testdata_test.go) once for each initial state of a test.
// Parse errors are not checked, since the tokenizer does not report them.
func TestTokenizerSuite(t *testing.T) {
	skipped := 0
	for _, f := range readTestFiles(t, "html5lib-tokenizer.json.gz") {
		if f.Name == "xmlViolation.test" {
			t.Logf("%s: not run; its outputs assume the coercion to an XML infoset of 13.2.9", f.Name)
			continue
		}
		var file struct {
			Tests []tokenizerTest `json:"tests"`
		}
		if err := json.Unmarshal([]byte(f.Text), &file, json.RejectUnknownMembers(true)); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		runs, passed, skips := 0, 0, 0
		t.Run(f.Name, func(t *testing.T) {
			for _, tt := range file.Tests {
				input, ok := tt.Input, true
				if tt.DoubleEscaped {
					input, ok = unescapeTest(input)
				}
				want, ok2 := expectedTokens(t, tt.Output, tt.DoubleEscaped)
				states := tt.InitialStates
				if len(states) == 0 {
					states = []string{"Data state"}
				}
				if !ok || !ok2 {
					skips++
					t.Run(tt.Description, func(t *testing.T) { t.Skip("a lone surrogate, which a Go string cannot hold") })
					continue
				}
				for _, name := range states {
					st, ok := testStates[name]
					if !ok {
						t.Fatalf("%s: unknown initial state %q", tt.Description, name)
					}
					runs++
					if t.Run(tt.Description+" in "+name, func(t *testing.T) {
						if got := tokensJSON(t, input, st, tt.LastStartTag); got != want {
							t.Errorf("input %+q, last start tag %q:\n got %s\nwant %s", input, tt.LastStartTag, got, want)
						}
					}) {
						passed++
					}
				}
			}
		})
		t.Logf("%s: %d tests, %d runs (by initial state), %d passed, %d tests skipped",
			f.Name, len(file.Tests), runs, passed, skips)
		skipped += skips
	}
	if skipped != tokenizerSkips {
		t.Errorf("skipped %d tests, want %d", skipped, tokenizerSkips)
	}
}

// tokensJSON tokenizes s from the state st and writes the tokens as the tests do: adjacent characters in one token, and
// no end-of-file token. Since no cdataAllowed is set, the tokenizer must not return two character tokens in a row.
func tokensJSON(t *testing.T, s string, st state, lastStartTag string) string {
	t.Helper()
	z := newTokenizer(s)
	z.setState(st)
	z.lastStartTag = lastStartTag
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)
	write := func(toks ...jsontext.Token) {
		t.Helper()
		for _, tok := range toks {
			if err := enc.WriteToken(tok); err != nil {
				t.Fatalf("input %+q: writing the tokens: %v", s, err)
			}
		}
	}
	nullable := func(s string, ok bool) jsontext.Token {
		if !ok {
			return jsontext.Null
		}
		return jsontext.String(s)
	}
	var text strings.Builder
	var prev tokenType
	write(jsontext.BeginArray)
	for {
		tok := z.next()
		if tok.typ == characterToken && (tok.data == "" || prev == characterToken) {
			t.Errorf("input %+q: a character token of %+q after a token of type %d", s, tok.data, prev)
		}
		prev = tok.typ
		if tok.typ == characterToken {
			text.WriteString(tok.data)
			continue
		}
		if text.Len() > 0 {
			write(jsontext.BeginArray, jsontext.String("Character"), jsontext.String(text.String()), jsontext.EndArray)
			text.Reset()
		}
		switch tok.typ {
		case doctypeToken:
			write(jsontext.BeginArray, jsontext.String("DOCTYPE"), nullable(tok.name, tok.name != ""),
				nullable(tok.publicID, tok.hasPublicID), nullable(tok.systemID, tok.hasSystemID),
				jsontext.Bool(!tok.forceQuirks), jsontext.EndArray)
		case startTagToken:
			write(jsontext.BeginArray, jsontext.String("StartTag"), jsontext.String(tok.name), jsontext.BeginObject)
			for _, a := range tok.attrs {
				if a.Namespace != "" {
					t.Fatalf("input %+q: attribute %q in namespace %q", s, a.Name, a.Namespace)
				}
				write(jsontext.String(a.Name), jsontext.String(a.Value))
			}
			write(jsontext.EndObject)
			if tok.selfClosing {
				write(jsontext.True)
			}
			write(jsontext.EndArray)
		case endTagToken:
			write(jsontext.BeginArray, jsontext.String("EndTag"), jsontext.String(tok.name), jsontext.EndArray)
		case commentToken:
			write(jsontext.BeginArray, jsontext.String("Comment"), jsontext.String(tok.data), jsontext.EndArray)
		case processingInstructionToken:
			write(jsontext.BeginArray, jsontext.String("ProcessingInstruction"), jsontext.String(tok.name),
				jsontext.String(tok.data), jsontext.EndArray)
		case characterToken:
		case eofToken:
			write(jsontext.EndArray)
			return buf.String()
		default:
			t.Fatalf("input %+q: token of type %d", s, tok.typ)
		}
	}
}

// expectedTokens returns the output of a test as tokensJSON writes it, after unescaping its strings again if
// doubleEscaped; ok is false if a string then has a lone surrogate.
func expectedTokens(t *testing.T, output jsontext.Value, doubleEscaped bool) (s string, ok bool) {
	t.Helper()
	dec := jsontext.NewDecoder(bytes.NewReader(output))
	var buf bytes.Buffer
	enc := jsontext.NewEncoder(&buf)
	for {
		tok, err := dec.ReadToken()
		if errors.Is(err, io.EOF) {
			return buf.String(), true
		}
		if err != nil {
			t.Fatalf("output %s: %v", output, err)
		}
		if doubleEscaped && tok.Kind() == jsontext.KindString {
			s, ok := unescapeTest(tok.String())
			if !ok {
				return "", false
			}
			tok = jsontext.String(s)
		}
		if err := enc.WriteToken(tok); err != nil {
			t.Fatalf("output %s: %v", output, err)
		}
	}
}

// unescapeTest replaces each backslash, 'u' and four hex digits in s with the code point (a surrogate pair with one),
// as the tests marked doubleEscaped ask; ok is false if a lone surrogate is left.
func unescapeTest(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, ok := hexEscape(s[i:])
		if !ok {
			b.WriteByte(s[i])
			i++
			continue
		}
		i += 6
		if utf16.IsSurrogate(r) {
			r2, ok := hexEscape(s[i:])
			if !ok {
				return "", false
			}
			if r = utf16.DecodeRune(r, r2); r == utf8.RuneError {
				return "", false
			}
			i += 6
		}
		b.WriteRune(r)
	}
	return b.String(), true
}

func hexEscape(s string) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(s[2:6], 16, 16)
	return rune(n), err == nil
}
