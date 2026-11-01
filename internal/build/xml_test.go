package build

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

// checkXML checks that data, the file name, is an XML file as the build writes them: the XML declaration, then one
// element that encoding/xml reads without errors, and nothing else.
func checkXML(t *testing.T, name string, data []byte) {
	t.Helper()
	if !bytes.HasPrefix(data, []byte(xmlHeader)) {
		t.Errorf("%s does not start with %q", name, xmlHeader)
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(tok)) > 0 {
				t.Errorf("%s has text outside its element: %q", name, tok)
			}
		}
	}
	if roots != 1 {
		t.Errorf("%s has %d elements at its root, want 1", name, roots)
	}
}

// TestXMLEscaping checks how character data, plain text in RSS and attribute values are escaped, and that
// encoding/xml reads back each of them, with U+FFFD for the characters that XML 1.0 does not allow.
func TestXMLEscaping(t *testing.T) {
	bad := string(utf8.RuneError)
	for _, tt := range []struct {
		name              string
		in                string
		text, plain, attr string
		read              string // what encoding/xml reads back
	}{
		{
			name:  "markup",
			in:    "a & b < c > d",
			text:  "a &amp; b &lt; c &gt; d",
			plain: "a &#x26; b &#x3C; c &#x3E; d",
			attr:  "a &amp; b &lt; c &gt; d",
			read:  "a & b < c > d",
		},
		{
			name:  "quotes",
			in:    `"q" 'a'`,
			text:  `"q" 'a'`,
			plain: `"q" 'a'`,
			attr:  "&quot;q&quot; &apos;a&apos;",
			read:  `"q" 'a'`,
		},
		{name: "end of CDATA", in: "]]>", text: "]]&gt;", plain: "]]&#x3E;", attr: "]]&gt;", read: "]]>"},
		{
			name:  "white space",
			in:    "tab\tLF\nCR\rCRLF\r\n",
			text:  "tab\tLF\nCR&#xD;CRLF&#xD;\n",
			plain: "tab\tLF\nCR&#xD;CRLF&#xD;\n",
			attr:  "tab&#x9;LF&#xA;CR&#xD;CRLF&#xD;&#xA;",
			read:  "tab\tLF\nCR\rCRLF\r\n",
		},
		{
			name:  "controls",
			in:    string(rune(0)) + "\x01\x1f\x7f" + string(rune(0x85)),
			text:  bad + bad + bad + "\x7f" + string(rune(0x85)),
			plain: bad + bad + bad + "\x7f" + string(rune(0x85)),
			attr:  bad + bad + bad + "\x7f" + string(rune(0x85)),
			read:  bad + bad + bad + "\x7f" + string(rune(0x85)),
		},
		{
			name:  "noncharacters",
			in:    string(rune(0xFFFE)) + string(rune(0xFFFF)) + string(rune(0xFDD0)) + string(rune(0x10FFFF)),
			text:  bad + bad + string(rune(0xFDD0)) + string(rune(0x10FFFF)),
			plain: bad + bad + string(rune(0xFDD0)) + string(rune(0x10FFFF)),
			attr:  bad + bad + string(rune(0xFDD0)) + string(rune(0x10FFFF)),
			read:  bad + bad + string(rune(0xFDD0)) + string(rune(0x10FFFF)),
		},
		{
			// A byte that cannot start a character, a surrogate, a cut character, and U+FFFD itself.
			name:  "invalid UTF-8",
			in:    "a\xffb\xed\xa0\x80c\xe6\x96" + bad,
			text:  "a" + bad + "b" + bad + bad + bad + "c" + bad + bad + bad,
			plain: "a" + bad + "b" + bad + bad + bad + "c" + bad + bad + bad,
			attr:  "a" + bad + "b" + bad + bad + bad + "c" + bad + bad + bad,
			read:  "a" + bad + "b" + bad + bad + bad + "c" + bad + bad + bad,
		},
		{name: "Japanese", in: "新サービス・〇〇", text: "新サービス・〇〇", plain: "新サービス・〇〇", attr: "新サービス・〇〇", read: "新サービス・〇〇"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var w xmlWriter
			w.text("a", tt.in, "b", tt.in)
			w.plain("c", tt.in)
			want := `<a b="` + tt.attr + `">` + tt.text + "</a>\n<c>" + tt.plain + "</c>\n"
			if got := w.String(); got != want {
				t.Errorf("escaped %q:\n%q\nwant:\n%q", tt.in, got, want)
			}
			doc := xmlHeader + "<x>\n" + w.String() + "</x>\n"
			checkXML(t, "the document", []byte(doc))
			var x struct {
				A struct {
					B    string `xml:"b,attr"`
					Text string `xml:",chardata"`
				} `xml:"a"`
				C string `xml:"c"`
			}
			if err := xml.Unmarshal([]byte(doc), &x); err != nil {
				t.Fatalf("reading %q: %v", doc, err)
			}
			if x.A.B != tt.read || x.A.Text != tt.read || x.C != tt.read {
				t.Errorf("read %q, %q and %q, want %q", x.A.B, x.A.Text, x.C, tt.read)
			}
		})
	}
}

// TestXMLWriter checks the lines that the writer writes for each kind of element.
func TestXMLWriter(t *testing.T) {
	var w xmlWriter
	w.WriteString(xmlHeader)
	w.start("a", "x", "1", "y", "<2>")
	w.empty("b", "z", "&")
	w.empty("c")
	w.text("d", "e\nf", "g", "h")
	w.plain("i", "j&k")
	w.end("a")
	want := strings.Join([]string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<a x="1" y="&lt;2&gt;">`,
		`<b z="&amp;"/>`,
		`<c/>`,
		`<d g="h">e`,
		`f</d>`,
		`<i>j&#x26;k</i>`,
		`</a>`,
		``,
	}, "\n")
	if got := w.String(); got != want {
		t.Errorf("wrote:\n%s\nwant:\n%s", got, want)
	}
	checkXML(t, "the document", w.Bytes())
}
