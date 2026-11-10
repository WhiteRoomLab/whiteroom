package wpformat

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitPages(t *testing.T) {
	const m = "<!--nextpage-->"
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"no page break", "a\n\nb", []string{"a\n\nb"}},
		{"empty", "", []string{""}},
		{"a page break", "a" + m + "b", []string{"a", "b"}},
		{"two page breaks", "a" + m + "b" + m + "c", []string{"a", "b", "c"}},
		{"line breaks around", "a\n" + m + "\nb", []string{"a", "b"}},
		{"a line break before", "a\n" + m + "b", []string{"a", "b"}},
		{"a line break after", "a" + m + "\nb", []string{"a", "b"}},
		{"two line breaks around", "a\n\n" + m + "\n\nb", []string{"a", "b"}},
		{"three line breaks around", "a\n\n\n" + m + "\n\n\nb", []string{"a\n", "\nb"}},
		{"two line breaks before and one after", "a\n\n" + m + "\nb", []string{"a", "b"}},
		{"page breaks in a row", "a" + m + m + "b", []string{"a", "", "b"}},
		{"page breaks in a row, with line breaks", "a\n" + m + "\n" + m + "\nb", []string{"a", "", "b"}},
		{"a page break at the start", m + "a", []string{"a"}},
		{"a line break and a page break at the start", "\n" + m + "a", []string{"a"}},
		{"a page break at the start, and another", m + "a" + m + "b", []string{"a", "b"}},
		{"two page breaks at the start", m + m + "a", []string{"", "a"}},
		{"a page break at the end", "a" + m, []string{"a", ""}},
		{"only a page break", m, []string{""}},
		{"block delimiters", "a<!-- wp:nextpage -->" + m + "<!-- /wp:nextpage -->b", []string{"a", "b"}},
		{"block delimiters with line breaks", "a\n<!-- wp:nextpage -->\n" + m + "\n<!-- /wp:nextpage -->\nb", []string{"a\n", "\nb"}},
		{"block delimiters and no page break", "a<!-- wp:nextpage --><!-- /wp:nextpage -->b", []string{"a<!-- wp:nextpage --><!-- /wp:nextpage -->b"}},
		{"a space in the page break", "a<!--nextpage -->b", []string{"a<!--nextpage -->b"}},
		{"a space before the page break", "a<!-- nextpage-->b", []string{"a<!-- nextpage-->b"}},
		{"in capitals", "a<!--NEXTPAGE-->b", []string{"a<!--NEXTPAGE-->b"}},
	}
	for _, tt := range tests {
		if got := SplitPages(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("%s: SplitPages(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

var splitBlocksTests = []struct {
	name string
	in   string
	max  int
	want []string
}{
	{"short", "abc", 10, []string{"abc"}},
	{"empty", "", 5, []string{""}},
	{"as long as the limit", "ab\ncd", 5, []string{"ab\ncd"}},
	{"the last place in range", "aaa\nbbb\nccc\n", 8, []string{"aaa\nbbb\n", "ccc\n"}},
	{"a place for each line", "aaa\nbbb\nccc\n", 4, []string{"aaa\n", "bbb\n", "ccc\n"}},
	{"between paragraphs", "aa\n\nbb\n\ncc", 5, []string{"aa\n\n", "bb\n\n", "cc"}},
	{"a line that is longer than the limit", "aaaaaaaaaa\nb\n", 5, []string{"aaaaaaaaaa\n", "b\n"}},
	{"a text without a place", "aaaaaaaaaa", 3, []string{"aaaaaaaaaa"}},
	{"a text with a place after the limit", "aaaa bbbb\ncc", 4, []string{"aaaa bbbb\n", "cc"}},
	{"a line break at the end only", "aaaaaaaaaa\n", 3, []string{"aaaaaaaaaa\n"}},
	{"a limit of 1", "a\nb\n", 1, []string{"a\n", "b\n"}},
	{"CR LF", "a\r\nb\r\n", 3, []string{"a\r\n", "b\r\n"}},
	{"div", "<div>\nx\n\ny\n</div>\nz\n", 6, []string{"<div>\nx\n\ny\n</div>\n", "z\n"}},
	{"div in capitals", "<DIV>\nx\n</DIV>\ny\n", 3, []string{"<DIV>\nx\n</DIV>\n", "y\n"}},
	{"nested divs", "<div>\n<div>\n</div>\n</div>\nz\n", 3, []string{"<div>\n<div>\n</div>\n</div>\n", "z\n"}},
	{"div that does not end", "<div>\na\nb\n", 3, []string{"<div>\na\nb\n"}},
	{"an end tag with no start tag", "</div>\na\n", 3, []string{"</div>\n", "a\n"}},
	{"an element with a name that starts like div", "<divx>\na\nb\n", 3, []string{"<divx>\n", "a\n", "b\n"}},
	{"a start tag that closes itself", "<div/>\nx\ny\n", 4, []string{"<div/>\n", "x\ny\n"}},
	{"table", "<table>\n<tr>\n</tr>\n</table>\nafter\n", 10, []string{"<table>\n<tr>\n</tr>\n</table>\n", "after\n"}},
	{"pre", "<pre>\n\n</pre>\nx\n", 4, []string{"<pre>\n\n</pre>\n", "x\n"}},
	{"script with a start tag in it", "<script>\nvar a = \"<div>\";\n</script>\nx\n", 3, []string{"<script>\nvar a = \"<div>\";\n</script>\n", "x\n"}},
	{"iframe with a start tag in it", "<iframe>\n<div>\n</iframe>\nx\n", 3, []string{"<iframe>\n<div>\n</iframe>\n", "x\n"}},
	{"a comment with a line break", "<!--\n-->\nx\n", 3, []string{"<!--\n-->\n", "x\n"}},
	{"a tag with a line break", "<a\nhref=x>t\nu\n", 3, []string{"<a\nhref=x>t\n", "u\n"}},
}

func TestSplitBlocks(t *testing.T) {
	for _, tt := range splitBlocksTests {
		got := SplitBlocks(tt.in, tt.max)
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: SplitBlocks(%q, %d) = %q, want %q", tt.name, tt.in, tt.max, got, tt.want)
		}
		if strings.Join(got, "") != tt.in {
			t.Errorf("%s: SplitBlocks(%q, %d) = %q, which do not make the text", tt.name, tt.in, tt.max, got)
		}
	}
}
