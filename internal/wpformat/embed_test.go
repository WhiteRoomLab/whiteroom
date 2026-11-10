package wpformat

import (
	"slices"
	"strings"
	"testing"
)

var autoembedTests = []struct {
	name, in string
	want     []string
}{
	{"empty", "", nil},
	{"no URL", "text\nmore", nil},
	{"only the scheme", "http://\nhttps://\n", nil},
	{"a scheme with nothing after it", "http\nhttps://\nhttp://", nil},
	{"a line", "text\nhttps://www.youtube.com/watch?v=abc\ntext", []string{"https://www.youtube.com/watch?v=abc"}},
	{"the only line", "http://a", []string{"http://a"}},
	{"the first line", "http://a\ntext", []string{"http://a"}},
	{"the last line", "text\nhttp://a", []string{"http://a"}},
	{"a line break first", "\nhttp://a", []string{"http://a"}},
	{"white space and blank lines", "  https://example.com/a  \n\nhttps://vimeo.com/1", []string{"https://example.com/a", "https://vimeo.com/1"}},
	{"lines in a row", "http://a\nhttp://b\n\nhttp://c\n\n\nhttp://d", []string{"http://a", "http://b", "http://c", "http://d"}},
	{"white space around lines", "http://a  \n  http://b", []string{"http://a", "http://b"}},
	{"upper case", "HTTPS://EXAMPLE.COM/E", []string{"HTTPS://EXAMPLE.COM/E"}},
	{"mixed case", "HtTp://A\nhTTPs://b", []string{"HtTp://A", "hTTPs://b"}},
	{"CR LF", "a\r\nhttp://a\r\n", []string{"http://a"}},
	{"a CR is not a line break", "a\rhttp://a\r", nil},
	{"a CR before the URL on the line", "\rhttp://a", []string{"http://a"}},
	{"a form feed", "\x0chttp://a\x0c", []string{"http://a"}},
	{"a vertical tab after it", "http://b\x0b\nx", []string{"http://b"}},
	{"a NUL belongs to the URL", "http://a\x00\nx", []string{"http://a\x00"}},
	{"a no-break space belongs to the URL", "http://c\xc2\xa0\nx", []string{"http://c\xc2\xa0"}},
	{"a no-break space is not white space after it", "http://c \xc2\xa0\nx", nil},
	{"text on the line after it", "http://a\n\tx", []string{"http://a"}},
	{"in a sentence", "x https://example.com/f", nil},
	{"two on a line", "http://a http://b\nhttp://c", []string{"http://c"}},
	{"text after it", "http://a b\nx", nil},
	{"a quote after it", "https://example.com/g?a=1&amp;b=2\"q", nil},
	{"a quote in it", "http://a\"b\nx", nil},
	{"a less-than sign after it", "http://a<b", nil},
	{"a greater-than sign after it", "http://a>\nx", nil},
	{"a greater-than sign before it", "> http://a", nil},
	{"an element before it on the line", "<b>http://a", nil},
	{"an element after it on the line", "http://a<b>", nil},
	{"an element line before it", "<b>\nhttp://a\n<b>", []string{"http://a"}},
	{"an element and a line break before it", "a\n<b>\nhttp://a", []string{"http://a"}},
	{"in an element", "<b>http://a</b>", nil},
	{"in an element, on its own line", "<b>http://a</b>\n", nil},
	{"after a comment", "<!-- a\nb -->\nhttp://c", []string{"http://c"}},
	{"in a comment, after a line break", "<!-- a\nb\nhttp://c -->", nil},
	{"after CDATA", "<![CDATA[x]]>\nhttp://c", []string{"http://c"}},
	{"in CDATA, after a line break", "<![CDATA[\nhttp://c\n]]>", nil},
	{"in an attribute, after a line break", "<a href=\"https://example.com/\nx\">https://example.com/b</a>", nil},
	{"after a start tag that has a line break", "<div\nclass=\"x\">\nhttps://example.com/c\n</div>", []string{"https://example.com/c"}},
	{"in pre", "<pre>\nhttps://example.com/d\n</pre>", []string{"https://example.com/d"}},
	{"after an image", "<img src=http://a>\nhttp://b", []string{"http://b"}},
	{"two paragraphs", "<p>http://a</p>\n<p>http://b</p>", []string{"http://a", "http://b"}},
	{"two paragraphs in a row", "<p>http://a</p><p>http://b</p>", []string{"http://a", "http://b"}},

	{"a paragraph", "<p>https://youtu.be/x</p>", []string{"https://youtu.be/x"}},
	{"a paragraph with attributes", `<p class="a"> https://x.com/a/status/1 </p>`, []string{"https://x.com/a/status/1"}},
	{"a paragraph with a space and no attributes", "<p >http://a</p>", []string{"http://a"}},
	{"a paragraph in upper case", "<P>HTTP://A</P>", []string{"HTTP://A"}},
	{"a paragraph that is on a line", "<p>\nhttp://a\n</p>", []string{"http://a"}},
	{"a paragraph with blank lines", "<p>\n\nhttp://a\n\n</p>", []string{"http://a"}},
	{"a paragraph with white space after the URL", "<p>http://a </p>", []string{"http://a"}},
	{"a paragraph after text", "text <p>http://a</p>", []string{"http://a"}},
	{"a paragraph and a URL after it", "<p>http://a</p>http://b", []string{"http://a"}},
	{"a paragraph and a line after it", "<p>http://a</p>\nhttp://b", []string{"http://a", "http://b"}},
	{"a paragraph after a start tag", "<p><p>http://a</p>", []string{"http://a"}},
	{"a paragraph after a start tag that has attributes", "<p x><p>http://a</p>", []string{"http://a"}},
	{"a paragraph start inside a start tag", "<p <p>http://a</p>", []string{"http://a"}},
	{"a paragraph start that ends an element", `<a title=<p>http://a</p>`, []string{"http://a"}},
	{"a paragraph start inside an element", `<a title="<p>">http://a</p>`, nil},
	{"a paragraph in a comment", "<!-- <p>http://u</p> -->", []string{"http://u"}},
	{"a paragraph in a comment, with a line break first", "<!--\n<p>http://u</p> -->", []string{"http://u"}},
	{"a paragraph in a comment, with line breaks", "<!-- <p>\nhttp://u\n</p> -->", nil},
	{"a paragraph in CDATA", "<![CDATA[<p>http://u</p>]]>", []string{"http://u"}},
	{"a paragraph that is not one", "<pre>http://a</pre>", nil},
	{"a paragraph with a slash", "<p/>http://a</p>", nil},
	{"a paragraph with a tab", "<p\t>http://a</p>", nil},
	{"a paragraph with a line break after the name", "<p\nclass=\"a\">http://a</p>", nil},
	{"a paragraph with a line break in the attributes", "<p class=\"a\nb\">http://a</p>", nil},
	{"a paragraph with a line break that is its end", "<p title=\"\nhttp://u</p>\">", []string{"http://u"}},
	{"a paragraph with a line break and white space", "<p title=\"\n  http://u  </p>\">", []string{"http://u"}},
	{"a paragraph with line breaks around the URL", "<p title=\"\n  http://u  \n</p>\">", nil},
	{"a paragraph with the wrong end tag", "<p>http://a</p >", nil},
	{"a paragraph with the wrong end tag, in a word", "<p>http://a</pre>", nil},
	{"a paragraph with no end tag", "<p>http://a", nil},
	{"an end tag only", "http://a</p>", nil},
	{"text in a paragraph", "<p>x http://a</p>", nil},
	{"a URL and text in a paragraph", "<p>http://a x</p>", nil},
	{"two URLs in a paragraph", "<p>http://a http://b</p>", nil},
	{"a link in a paragraph", `<p><a href="http://a">http://a</a></p>`, nil},
	{"a URL that was replaced", "<p>{{http://a}}</p>", nil},
	{"a paragraph and the lines around it", "http://a\n<p>http://b</p>\nhttp://c", []string{"http://a", "http://b", "http://c"}},
	{"a paragraph that is also a line is one URL", "<p>\nhttp://a</p>\n", []string{"http://a"}},
	{"the URL after the paragraph start tag has two lines", "<p>http://a\nhttp://b</p>", nil},
	{"paragraphs far apart", "<p>x</p>" + strings.Repeat("y", 100) + "<p>http://a</p>", []string{"http://a"}},
}

func TestAutoembedURLs(t *testing.T) {
	for _, tt := range autoembedTests {
		t.Run(tt.name, func(t *testing.T) {
			got := AutoembedURLs(tt.in)
			var urls []string
			end := 0
			for _, span := range got {
				if span.Start < end || span.End <= span.Start || span.End > len(tt.in) {
					t.Fatalf("the range %d:%d is wrong after %d in %q", span.Start, span.End, end, tt.in)
				}
				end = span.End
				urls = append(urls, tt.in[span.Start:span.End])
			}
			if !slices.Equal(urls, tt.want) {
				t.Errorf("AutoembedURLs(%q) = %q, want %q", tt.in, urls, tt.want)
			}
			if plain := referenceAutoembed(tt.in); !slices.Equal(got, plain) {
				t.Errorf("AutoembedURLs(%q) = %v, and the plain search made %v", tt.in, got, plain)
			}
		})
	}
}

func TestAutoembedURLsRanges(t *testing.T) {
	s := "ab\n  http://a/b  \n<p class=\"x\"> HTTPS://c </p> http://no"
	got := AutoembedURLs(s)
	want := []URLSpan{{Start: 5, End: 15}, {Start: 32, End: 41}}
	if !slices.Equal(got, want) {
		t.Errorf("AutoembedURLs(%q) = %v, want %v", s, got, want)
	}
}
