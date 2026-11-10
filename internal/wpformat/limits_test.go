package wpformat

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// The time that Autop, SplitBlocks, and SplitPages take grows with the length of the text. They count the bytes that they
// look at, so that a test can say so without a clock, and the count does not change with -race.
const (
	// scanFactor is how many bytes the functions look at for each byte of a text that does not grow.
	scanFactor = 128
	// workFactor is how many bytes Autop looks at for each byte of the longest text that its steps work on.
	workFactor = 32
	// growthFactor is how many times longer than the text the longest text in Autop is: a line break in an element
	// becomes 15 bytes, and the line breaks that Autop adds before and after elements are in the elements too.
	growthFactor = 24
	scanSlack    = 1 << 16
)

// metered runs f with a meter that stops it after budget bytes, and says whether f finished. A slow run that was not
// stopped would hold up the test for minutes.
func metered(budget int, f func(m *meter)) (m meter, finished bool) {
	m.budget = budget
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(overBudget); !ok {
				panic(r)
			}
		}
	}()
	f(&m)
	return m, true
}

// autopBudget is far above what a text of n bytes needs, as the limits of the tests say.
func autopBudget(n int) int { return workFactor*growthFactor*n + scanSlack }

func scanSizes() []int {
	if raceEnabled {
		return []int{1 << 18, 1 << 20}
	}
	return []int{1 << 20, 4 << 20}
}

// rep repeats unit until the text is about n bytes long.
func rep(unit string, n int) string {
	return strings.Repeat(unit, max(n/len(unit), 1))
}

var autopInputs = []struct {
	name string
	make func(n int) string
	// plain is set where the text does not grow much while Autop works on it.
	plain bool
}{
	{"spaces before a line break", func(n int) string { return "a" + strings.Repeat(" ", n) + "\nb" }, true},
	{"mixed white space before a line break", func(n int) string { return "a" + rep(" \t\v\f", n) + "\nb" }, true},
	{"line breaks and spaces", func(n int) string { return rep(" \n", n) }, true},
	{"CR LF", func(n int) string { return rep("a\r\n", n) }, true},
	{"p li", func(n int) string { return rep("<p><li", n) }, true},
	{"p li and line breaks, and an end tag of p far away", func(n int) string { return rep("<p><li\n", n) + "</p>" }, false},
	{"p blockquote and a quote", func(n int) string { return rep("<p><blockquote \"", n) }, true},
	{"p blockquote and a single quote", func(n int) string { return rep("<p><blockquote class='", n) }, true},
	{"script that does not end", func(n int) string { return rep("<script>", n) }, true},
	{"style that does not end", func(n int) string { return rep("<style>", n) }, true},
	{"svg that does not end", func(n int) string { return rep("<svg>", n) }, true},
	{"math that does not end", func(n int) string { return rep("<math>", n) }, true},
	{"pre", func(n int) string { return rep("<pre>x</pre>", n) }, true},
	{"pre that does not end", func(n int) string { return rep("<pre>x", n) }, true},
	{"end tags of pre", func(n int) string { return rep("x</pre>", n) }, true},
	{"br", func(n int) string { return rep("<br>", n) }, true},
	{"br with a slash, a space and a line break", func(n int) string { return rep("<br /> \n", n) }, true},
	{"br and long white space", func(n int) string { return rep("<br />"+strings.Repeat(" ", 100), n) }, true},
	{"openings of comments", func(n int) string { return rep("<!--", n) }, true},
	{"less-than signs", func(n int) string { return rep("<", n) }, true},
	{"p", func(n int) string { return rep("<p>", n) }, true},
	{"p and long white space", func(n int) string { return rep("<p>"+strings.Repeat(" ", 100), n) }, true},
	{"end tags of p", func(n int) string { return rep("</p>", n) }, true},
	{"param that does not end, and white space", func(n int) string { return rep("<param", n/2) + ">" + strings.Repeat(" ", n/2) + "x" }, true},
	{"div that does not end, and white space", func(n int) string { return rep("<div", n/2) + ">" + strings.Repeat(" ", n/2) + "x" }, true},
	{"p and div that do not end", func(n int) string { return rep("<p><div ", n) }, true},
	{"p, text, and the end tag of div", func(n int) string { return rep("<p>a</div>", n) }, true},
	{"hr and long white space", func(n int) string { return rep("<hr"+strings.Repeat(" ", 100), n) }, true},
	{"option that does not end", func(n int) string { return rep("<option ", n) }, true},
	{"object that does not end", func(n int) string { return rep("<object ", n) + "</object>" }, true},
	{"figcaption that does not end", func(n int) string { return rep("<figcaption ", n) }, true},
	{"audio and source", func(n int) string { return rep("<audio ", n) + "<source" }, true},
	{"placeholders for a line break", func(n int) string { return rep(" <!-- wpnl --> ", n) }, true},
	{"tags with a line break", func(n int) string { return rep("<a\n>", n) }, false},
	{"one tag with line breaks", func(n int) string { return "<a" + strings.Repeat("\n", n) }, false},
	{"less-than signs and line breaks", func(n int) string { return rep("<\n", n) }, false},
	{"comment with hr tags", func(n int) string { return "<!--" + rep("<hr>", n) }, false},
}

func TestAutopLimits(t *testing.T) {
	for _, tt := range autopInputs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range scanSizes() {
				doc := tt.make(size)
				var got string
				m, finished := metered(autopBudget(len(doc)), func(m *meter) { got = autop(doc, m) })
				if !finished {
					t.Errorf("%d bytes: looked at more than %d bytes", len(doc), m.budget)
					continue
				}
				if limit := growthFactor*len(doc) + scanSlack; m.longest > limit {
					t.Errorf("%d bytes: the longest text has %d bytes, want at most %d", len(doc), m.longest, limit)
				}
				if limit := workFactor*m.longest + scanSlack; m.scanned > limit {
					t.Errorf("%d bytes: looked at %d bytes of a text of %d bytes, want at most %d", len(doc), m.scanned, m.longest, limit)
				}
				if limit := scanFactor*len(doc) + scanSlack; tt.plain && m.scanned > limit {
					t.Errorf("%d bytes: looked at %d bytes, want at most %d", len(doc), m.scanned, limit)
				}
				if tt.name == "pre" {
					if n, want := strings.Count(got, "<pre>x</pre>"), len(doc)/len("<pre>x</pre>"); n != want {
						t.Errorf("%d bytes: %d pre elements in the result, want %d", len(doc), n, want)
					}
				}
			}
		})
	}
}

var blockInputs = []struct {
	name string
	make func(n int) string
}{
	{"div that does not end", func(n int) string { return rep("<div>\n", n) }},
	{"divs", func(n int) string { return rep("<div>\n</div>\n", n) }},
	{"end tags of div", func(n int) string { return rep("</div>\n", n) }},
	{"lines", func(n int) string { return rep("a\n", n) }},
	{"p", func(n int) string { return rep("<p>\n", n) }},
	{"script that does not end", func(n int) string { return rep("<script>\n", n) }},
	{"scripts", func(n int) string { return rep("<script>\n</script>\n", n) }},
	{"title and a script end tag", func(n int) string { return rep("<title>\n", n) + "</script>" }},
	{"less-than signs", func(n int) string { return rep("<", n) }},
	{"less-than signs and line breaks", func(n int) string { return rep("<\n", n) }},
	{"openings of comments", func(n int) string { return rep("<!--\n", n) }},
	{"long line", func(n int) string { return strings.Repeat("a", n) + "\n" }},
}

func TestSplitBlocksLimits(t *testing.T) {
	for _, tt := range blockInputs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range scanSizes() {
				doc := tt.make(size)
				for _, limit := range []int{1, 64, 1 << 16} {
					var parts []string
					m, finished := metered(4*scanFactor*len(doc)+scanSlack, func(m *meter) { parts = splitBlocks(doc, limit, m) })
					if !finished {
						t.Errorf("%d bytes, max %d: looked at more than %d bytes", len(doc), limit, m.budget)
						continue
					}
					if want := scanFactor*len(doc) + scanSlack; m.scanned > want {
						t.Errorf("%d bytes, max %d: looked at %d bytes, want at most %d", len(doc), limit, m.scanned, want)
					}
					if strings.Join(parts, "") != doc {
						t.Errorf("%d bytes, max %d: the parts do not make the text", len(doc), limit)
					}
				}
			}
		})
	}
}

var pageInputs = []struct {
	name  string
	make  func(n int) string
	pages int
}{
	{"page breaks", func(n int) string { return rep("<!--nextpage-->", n) }, -1},
	{"page breaks and line breaks", func(n int) string { return rep("\n<!--nextpage-->\n", n) }, -1},
	{"text and page breaks", func(n int) string { return rep("a<!--nextpage-->", n) }, -1},
	{"block delimiters", func(n int) string { return rep("<!-- wp:nextpage -->", n) }, 1},
	{"no page break", func(n int) string { return rep("<!--nextpage", n) }, 1},
}

func TestSplitPagesLimits(t *testing.T) {
	for _, tt := range pageInputs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range scanSizes() {
				doc := tt.make(size)
				var pages []string
				m, finished := metered(4*scanFactor*len(doc)+scanSlack, func(m *meter) { pages = splitPages(doc, m) })
				if !finished {
					t.Errorf("%d bytes: looked at more than %d bytes", len(doc), m.budget)
					continue
				}
				if want := scanFactor*len(doc) + scanSlack; m.scanned > want {
					t.Errorf("%d bytes: looked at %d bytes, want at most %d", len(doc), m.scanned, want)
				}
				if tt.pages > 0 && len(pages) != tt.pages {
					t.Errorf("%d bytes: %d pages, want %d", len(doc), len(pages), tt.pages)
				}
			}
		})
	}
}

// distinct makes n bytes of texts with names that are all different: [n0], [n1], and so on.
func distinct(n int, format string) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, format, i)
	}
	return b.String()
}

// scanInputs are the texts that make FindShortcodes, ParseAttrs, and AutoembedURLs slow if one of them starts a search
// again from a place that it has been to.
var scanInputs = []struct {
	name string
	make func(n int) string
}{
	{"openings that do not end", func(n int) string { return rep("[foo", n) }},
	{"shortcodes with no closing tag", func(n int) string { return rep("[foo]", n) }},
	{"closing tags", func(n int) string { return rep("[/foo]", n) }},
	{"long attributes with no bracket", func(n int) string { return "[foo " + strings.Repeat("a", n) }},
	{"long attributes with slashes", func(n int) string { return "[foo " + strings.Repeat("/", n) + "]" }},
	{"double brackets", func(n int) string { return rep("[[", n) }},
	{"escaped shortcodes", func(n int) string { return rep("[[foo]]", n) }},
	{"less-than signs", func(n int) string { return rep("<", n) }},
	{"URL lines", func(n int) string { return rep("http://a\n", n) }},
	{"URL lines with text after the URL", func(n int) string { return rep("http://a b\n", n) }},
	{"paragraph starts", func(n int) string { return rep("<p>", n) }},

	{"shortcodes and closing tags of another name", func(n int) string { return rep("[foo][/bar]", n) }},
	{"shortcodes, and then closing tags of another name", func(n int) string { return rep("[foo]", n/2) + rep("[/bar]", n/2) }},
	{"shortcodes and one closing tag", func(n int) string { return rep("[foo]", n) + "[/foo]" }},
	{"shortcodes with a closing tag each", func(n int) string { return rep("[foo]x[/foo]", n) }},
	{"shortcodes with names that are all different", func(n int) string { return distinct(n, "[n%d]") }},
	{"names that are all different, and closing tags", func(n int) string { return distinct(n/2, "[n%d]") + distinct(n/2, "[/m%d]") }},
	{"names that are all different, with closing tags", func(n int) string { return distinct(n/2, "[n%[1]d]x[/n%[1]d]") }},
	{"brackets in elements", func(n int) string { return rep(`<a title="[">`, n) }},
	{"close brackets in elements", func(n int) string { return "[foo " + rep(`<a title="]">`, n) }},
	{"shortcodes and brackets in elements", func(n int) string { return rep(`<b title="[x]">[foo]x<i title="[/foo]">`, n) }},
	{"an attribute list over elements", func(n int) string { return "[foo " + rep("<b>", n) }},
	{"a shortcode and elements", func(n int) string { return "[foo]" + rep("<b>", n) + "[/foo]" }},
	{"names with long runs", func(n int) string { return rep("["+strings.Repeat("a", 200), n) }},
	{"brackets with long runs", func(n int) string { return rep("[["+strings.Repeat("a", 200), n) }},

	{"quotes of attributes that do not end", func(n int) string { return rep(`a="`, n) }},
	{"single quotes that do not end", func(n int) string { return rep(`a='`, n) }},
	{"names with equals signs", func(n int) string { return rep("a=", n) }},
	{"names", func(n int) string { return rep("a ", n) }},
	{"double quotes", func(n int) string { return rep(`"`, n) }},
	{"single quotes", func(n int) string { return rep("'", n) }},
	{"backslashes", func(n int) string { return "a=" + strings.Repeat(`\`, n) }},
	{"backslashes and escapes", func(n int) string { return "a=" + rep(`\x4\1\\`, n) }},
	{"a value with less-than signs", func(n int) string { return "a=" + strings.Repeat("<", n) }},
	{"values with elements", func(n int) string { return rep("a=<b>", n) }},
	{"values with starts of elements", func(n int) string { return rep("a=<b ", n) }},
	{"attributes", func(n int) string { return rep("a=b ", n) }},
	{"attributes that are different", func(n int) string { return distinct(n, "n%d=v ") }},
	{"names, white space, and equals signs", func(n int) string { return rep("a  =  ", n) }},
	{"quoted values", func(n int) string { return rep(`a="x" `, n) }},
	{"quoted values with no space after them", func(n int) string { return rep(`a="x"`, n) }},
	{"white space after a name", func(n int) string { return "a" + strings.Repeat(" ", n) + "x" }},
	{"white space before the equals sign", func(n int) string { return "a" + strings.Repeat(" ", n) + "=b" }},
	{"white space after the equals sign", func(n int) string { return "a=" + strings.Repeat(" ", n) + `"b` }},
	{"no-break spaces", func(n int) string { return rep("\xc2\xa0", n) }},
	{"no-break spaces and values", func(n int) string { return rep("a=\xc2\xa0b\xe2\x80\x8b", n) }},
	{"quotes with white space", func(n int) string { return `"` + strings.Repeat(" ", n) }},

	{"URLs with white space after them", func(n int) string { return rep("http://a  ", n) }},
	{"URLs with line breaks before them", func(n int) string { return rep("\nhttp://a", n) }},
	{"URLs after paragraph starts", func(n int) string { return rep("<p>http://a", n) }},
	{"paragraphs", func(n int) string { return rep("<p>http://a</p>", n) }},
	{"paragraphs on lines", func(n int) string { return rep("<p>\nhttp://a\n</p>\n", n) }},
	{"paragraph starts with spaces", func(n int) string { return rep("<p ", n) }},
	{"paragraph starts with attributes", func(n int) string { return rep("<p x", n) }},
	{"paragraph starts with line breaks", func(n int) string { return rep("<p \n", n) }},
	{"paragraph starts, an end, and white space", func(n int) string { return rep("<p ", n/2) + ">" + strings.Repeat(" ", n/2) + "x" }},
	{"paragraph starts, an end, white space, and a URL", func(n int) string { return rep("<p ", n/2) + ">" + strings.Repeat(" ", n/2) + "http://a</p>" }},
	{"paragraph starts in an element", func(n int) string { return "<a " + rep("<p ", n) }},
	{"schemes", func(n int) string { return rep("http://", n) }},
	{"half schemes", func(n int) string { return rep("http:/", n) }},
	{"line breaks", func(n int) string { return strings.Repeat("\n", n) }},
	{"spaces and line breaks", func(n int) string { return rep(" \n", n) }},
	{"URLs on lines in elements", func(n int) string { return rep("<a\nhttp://a\n>", n) }},
	{"URLs in comments", func(n int) string { return "<!--" + rep("\nhttp://a\n", n) }},
	{"openings of comments", func(n int) string { return rep("<!--", n) }},
	{"openings of CDATA", func(n int) string { return rep("<![CDATA[", n) }},
}

var scanners = []struct {
	name string
	run  func(doc string, m *meter)
}{
	{"FindShortcodes with every name", func(doc string, m *meter) { findShortcodes(doc, acceptAll, m) }},
	{"FindShortcodes with foo", func(doc string, m *meter) {
		findShortcodes(doc, func(name string, _ byte) bool { return name == "foo" }, m)
	}},
	{"ParseAttrs", func(doc string, m *meter) { parseAttrs(doc, m) }},
	{"AutoembedURLs", func(doc string, m *meter) { autoembedURLs(doc, m) }},
	// A text with no "://" is not searched at all, so the same texts are tried with a line that has it.
	{"AutoembedURLs with a scheme first", func(doc string, m *meter) { autoembedURLs("x://\n"+doc, m) }},
}

func TestScanLimits(t *testing.T) {
	for _, tt := range scanInputs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range scanSizes() {
				doc := tt.make(size)
				for _, sc := range scanners {
					m, finished := metered(4*scanFactor*len(doc)+scanSlack, func(m *meter) { sc.run(doc, m) })
					if !finished {
						t.Errorf("%s, %d bytes: looked at more than %d bytes", sc.name, len(doc), m.budget)
					} else if want := scanFactor*len(doc) + scanSlack; m.scanned > want {
						t.Errorf("%s, %d bytes: looked at %d bytes, want at most %d", sc.name, len(doc), m.scanned, want)
					}
				}
			}
		})
	}
}

func TestScanResults(t *testing.T) {
	const n = 100000
	if got := FindShortcodes(rep("[foo]", 5*n), acceptAll); len(got) != n || got[0].Closed || got[n-1].End != 5*n {
		t.Errorf("FindShortcodes of %d open shortcodes found %d", n, len(got))
	}
	if got := FindShortcodes(rep("[foo]x[/foo]", 12*n), acceptAll); len(got) != n || !got[0].Closed || got[n-1].End != 12*n {
		t.Errorf("FindShortcodes of %d closed shortcodes found %d", n, len(got))
	}
	if got := FindShortcodes(rep(`<a title="[">[foo]`, 18*n), acceptAll); len(got) != n {
		t.Errorf("FindShortcodes of %d shortcodes next to elements found %d", n, len(got))
	}
	if got := FindShortcodes(rep("[foo]", n)+"[/foo]", acceptAll); len(got) != 1 || !got[0].Closed {
		t.Errorf("FindShortcodes of one shortcode that holds %d others found %d", n, len(got))
	}
	if got := AutoembedURLs(rep("http://a\n", 9*n)); len(got) != n {
		t.Errorf("AutoembedURLs of %d lines found %d", n, len(got))
	}
	if got := AutoembedURLs(rep("<p>http://a</p>", 15*n)); len(got) != n {
		t.Errorf("AutoembedURLs of %d paragraphs found %d", n, len(got))
	}
	if got := ParseAttrs(rep("a=b ", 4*n)); len(got) != 1 {
		t.Errorf("ParseAttrs of one name found %d attributes", len(got))
	}
	if got := ParseAttrs(distinct(8*n, "n%d=v ")); len(got) < n/2 {
		t.Errorf("ParseAttrs of different names found %d attributes", len(got))
	}
}

// TestScanMemory checks that the functions do not make texts that are many times as long as the text. The factors are
// small where the result is small, and larger where the result is a list with a few words for each few bytes of text (the
// total of memory is more than the list because a list that grows is made again).
func TestScanMemory(t *testing.T) {
	const size = 1 << 20
	tests := []struct {
		name   string
		doc    string
		run    func(doc string)
		factor uint64
	}{
		{"openings", rep("[foo", size), func(doc string) { FindShortcodes(doc, acceptAll) }, 1},
		{"brackets in elements", rep(`<a title="[]">`, size), func(doc string) { FindShortcodes(doc, acceptAll) }, 2},
		{"closing tags of one name", "[foo]" + rep("[/x]", size), func(doc string) { FindShortcodes(doc, acceptAll) }, 12},
		{"closing tags of names that are all different", "[foo]" + distinct(size, "[/n%d]"), func(doc string) { FindShortcodes(doc, acceptAll) }, 25},
		{"no-break spaces", `a="` + rep("\xc2\xa0b", size) + `"`, func(doc string) { ParseAttrs(doc) }, 2},
		{"escapes", "a=" + rep(`\x41\n`, size), func(doc string) { ParseAttrs(doc) }, 2},
		{"one long attribute", "a=" + strings.Repeat("b", size), func(doc string) { ParseAttrs(doc) }, 1},
		{"lines", "://" + rep("x\n", size), func(doc string) { AutoembedURLs(doc) }, 1},
		{"line breaks in elements", "://" + rep("<a\n>", size), func(doc string) { AutoembedURLs(doc) }, 1},
		{"paragraph starts", "://" + rep("<p ", size), func(doc string) { AutoembedURLs(doc) }, 1},
		{"URL lines", rep("http://a\n", size), func(doc string) { AutoembedURLs(doc) }, 10},
	}
	for _, tt := range tests {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		tt.run(tt.doc)
		runtime.ReadMemStats(&after)
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > tt.factor*uint64(len(tt.doc))+1<<20 {
			t.Errorf("%s: %d bytes made %d bytes of memory, want at most %d times as many and 1 MiB", tt.name, len(tt.doc), allocated, tt.factor)
		}
	}
}
