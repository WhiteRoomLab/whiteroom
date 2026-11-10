package wpformat

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

func onlyTrimmed(s string) bool {
	return !slices.ContainsFunc([]byte(s), func(c byte) bool { return !isTrim(c) })
}

// checkAutop checks what holds for any text.
func checkAutop(t testing.TB, s string) {
	t.Helper()
	var got string
	m, finished := metered(autopBudget(len(s)), func(m *meter) { got = autop(s, m) })
	if !finished {
		t.Fatalf("Autop(%q) looked at more than %d bytes", s, m.budget)
	}
	if again := Autop(s); again != got {
		t.Fatalf("Autop(%q) = %q, and then %q", s, got, again)
	}
	if onlyTrimmed(s) && got != "" {
		t.Errorf("Autop(%q) = %q, want an empty text for a text of white space", s, got)
	}
	if strings.Contains(got, wpnl) {
		t.Errorf("Autop(%q) = %q, which has the text for a line break in an element", s, got)
	}
	if limit := growthFactor*len(s) + scanSlack; m.longest > limit {
		t.Errorf("Autop(%q): the longest text has %d bytes, want at most %d", s, m.longest, limit)
	}
	if limit := workFactor*m.longest + scanSlack; m.scanned > limit {
		t.Errorf("Autop(%q) looked at %d bytes of a text of %d bytes, want at most %d", s, m.scanned, m.longest, limit)
	}
}

func fuzzAutopSeeds() []string {
	seeds := []string{
		"", "<", "<!--", "<![CDATA[", "<pre", "</pre>", "<pre></pre>", "<br><br>", "<p><li", "<p><blockquote \"", "<script>\n",
		"<svg>\n</svg", "<hr", "<option", "</object>", "<audio><source", "<figcaption", "\n\n\n", "\r\n", " <!-- wpnl --> ",
		"<WPPreserveNewline />", "<a\n\n\n>", "<p><div", "</p></p>\n</p>", "a<br /> \nb", "<pre wp-pre-tag-0></pre>",
	}
	for _, tt := range autopTests {
		seeds = append(seeds, tt.in)
	}
	return seeds
}

func FuzzAutop(f *testing.F) {
	for _, s := range fuzzAutopSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { checkAutop(t, s) })
}

func TestAutopDocuments(t *testing.T) {
	for _, s := range fuzzAutopSeeds() {
		checkAutop(t, s)
	}
	for _, s := range autopDocuments() {
		checkAutop(t, s)
	}
}

// referenceParts splits s in the way that SplitBlocks says it does, with a search for each part.
func referenceParts(s string, points []int, limit int) []string {
	if len(s) <= limit || s == "" {
		return []string{s}
	}
	var parts []string
	for start := 0; start < len(s); {
		if len(s)-start <= limit {
			parts = append(parts, s[start:])
			break
		}
		// The first place after start+limit, and the one before it.
		i := sort.SearchInts(points, start+limit+1)
		end := len(s)
		switch {
		case i > 0 && points[i-1] > start:
			end = points[i-1]
		case i < len(points):
			end = points[i]
		}
		parts = append(parts, s[start:end])
		start = end
	}
	return parts
}

// checkSplitBlocks checks what holds for any text and any length.
func checkSplitBlocks(t testing.TB, s string, limit int) {
	t.Helper()
	var parts []string
	m, finished := metered(4*scanFactor*len(s)+scanSlack, func(m *meter) { parts = splitBlocks(s, limit, m) })
	if !finished {
		t.Fatalf("SplitBlocks(%q, %d) looked at more than %d bytes", s, limit, m.budget)
	}
	if got := strings.Join(parts, ""); got != s {
		t.Fatalf("SplitBlocks(%q, %d) = %q, which do not make the text", s, limit, parts)
	}
	if again := SplitBlocks(s, limit); !slices.Equal(again, parts) {
		t.Fatalf("SplitBlocks(%q, %d) = %q, and then %q", s, limit, parts, again)
	}
	if want := scanFactor*len(s) + scanSlack; m.scanned > want {
		t.Errorf("SplitBlocks(%q, %d) looked at %d bytes, want at most %d", s, limit, m.scanned, want)
	}
	points := splitPoints(nil, s)
	for i, p := range points {
		if p <= 0 || p >= len(s) || s[p-1] != '\n' || i > 0 && p <= points[i-1] {
			t.Fatalf("splitPoints(%q) = %v, want places after a line break, in order", s, points)
		}
	}
	if want := referenceParts(s, points, limit); !slices.Equal(parts, want) {
		t.Errorf("SplitBlocks(%q, %d) = %q, want %q", s, limit, parts, want)
	}
	start := 0
	for _, p := range parts {
		end := start + len(p)
		if i := sort.SearchInts(points, start+1); len(p) > limit && i < len(points) && points[i] < end {
			t.Errorf("SplitBlocks(%q, %d) has the part %q that is longer than the limit, and has the place %d in it", s, limit, p, points[i])
		}
		if end < len(s) {
			if _, ok := slices.BinarySearch(points, end); !ok {
				t.Errorf("SplitBlocks(%q, %d) splits at %d, which is not a place for it (%v)", s, limit, end, points)
			}
		}
		if p == "" && s != "" {
			t.Errorf("SplitBlocks(%q, %d) has an empty part", s, limit)
		}
		start = end
	}
}

func FuzzSplitBlocks(f *testing.F) {
	for _, s := range fuzzAutopSeeds() {
		f.Add(s, uint8(len(s)/2))
	}
	for _, tt := range splitBlocksTests {
		f.Add(tt.in, uint8(tt.max))
	}
	f.Fuzz(func(t *testing.T, s string, size uint8) {
		for _, limit := range []int{1, 7, 64, int(size)} {
			checkSplitBlocks(t, s, limit)
		}
	})
}

func TestSplitBlocksDocuments(t *testing.T) {
	for _, s := range fuzzAutopSeeds() {
		for _, limit := range []int{1, 7, 64} {
			checkSplitBlocks(t, s, limit)
		}
	}
	for i, s := range autopDocuments() {
		if i%3 == 0 {
			for _, limit := range []int{1, 5, 40, 200} {
				checkSplitBlocks(t, s, limit)
			}
		}
	}
}

// The longest texts that the checks compare with the plain search, which takes much longer than the search of the package.
const (
	plainShortcodesLimit = 3000
	plainAttrsLimit      = 300
	plainEmbedLimit      = 1500
)

var shortcodeRules = []struct {
	name   string
	isName func(name string, next byte) bool
}{
	{"every name", acceptAll},
	{"foo", func(name string, _ byte) bool { return name == "foo" }},
	{"a name with o, before a space or a bracket", func(name string, next byte) bool {
		return strings.Contains(name, "o") && (next == ' ' || next == ']')
	}},
}

// checkShortcodes checks what holds for any text.
func checkShortcodes(t testing.TB, s string, isName func(string, byte) bool) {
	t.Helper()
	var got []Shortcode
	m, finished := metered(4*scanFactor*len(s)+scanSlack, func(m *meter) { got = findShortcodes(s, isName, m) })
	if !finished {
		t.Fatalf("FindShortcodes(%q) looked at more than %d bytes", s, m.budget)
	}
	if again := FindShortcodes(s, isName); !slices.Equal(again, got) {
		t.Fatalf("FindShortcodes(%q) = %+v, and then %+v", s, got, again)
	}
	if want := scanFactor*len(s) + scanSlack; m.scanned > want {
		t.Errorf("FindShortcodes(%q) looked at %d bytes, want at most %d", s, m.scanned, want)
	}
	end := 0
	for _, c := range got {
		if c.Start < end || c.End <= c.Start || c.End > len(s) || c.Name == "" {
			t.Fatalf("FindShortcodes(%q) = %+v, which has a wrong range or name", s, got)
		}
		end = c.End
		nameEnd := c.Start + 1 + len(c.Name)
		if c.Open {
			nameEnd++
		}
		var next byte
		if nameEnd < len(s) {
			next = s[nameEnd]
		}
		if !isName(c.Name, next) || strings.ContainsFunc(c.Name, func(r rune) bool { return r > 127 || !isNameByte(byte(r)) }) {
			t.Fatalf("FindShortcodes(%q) = %+v, which has a name that is wrong", s, got)
		}
		var text strings.Builder
		if c.Open {
			text.WriteByte('[')
		}
		text.WriteString("[" + c.Name + c.Attrs)
		if c.SelfClosing {
			text.WriteByte('/')
		}
		text.WriteByte(']')
		if c.Closed {
			text.WriteString(c.Content + "[/" + c.Name + "]")
		}
		if c.Close {
			text.WriteByte(']')
		}
		if s[c.Start:c.End] != text.String() || c.SelfClosing && c.Closed || !c.Closed && c.Content != "" {
			t.Fatalf("FindShortcodes(%q) = %+v, which is not in the form of its flags", s, got)
		}
	}
	if len(s) <= plainShortcodesLimit {
		if want := referenceShortcodes(s, isName); !slices.Equal(got, want) {
			t.Errorf("FindShortcodes(%q) = %+v, and the plain search made %+v", s, got, want)
		}
	}
}

func fuzzShortcodeSeeds() []string {
	seeds := []string{
		"[", "]", "[[", "[foo", "[foo]", "[[foo]]", "[foo]x[/foo]", "[foo/]", "<a title=\"[foo]\">[foo]", "[foo]<b title=\"[/foo]\">[/foo]",
		"<!-- [foo] -->", "<![CDATA[ [foo] ]]>", "[foo a=\"x]y\"]", "[foo-bar][/foo-bar]", "[fo<b>o]", "[foo <b/>]", "<", "<!--",
	}
	for _, tt := range findShortcodeTests {
		seeds = append(seeds, tt.in)
	}
	return seeds
}

func FuzzShortcodes(f *testing.F) {
	for i, s := range fuzzShortcodeSeeds() {
		f.Add(s, uint8(i))
	}
	f.Fuzz(func(t *testing.T, s string, rule uint8) {
		checkShortcodes(t, s, shortcodeRules[int(rule)%len(shortcodeRules)].isName)
	})
}

// checkParseAttrs checks what holds for any text.
func checkParseAttrs(t testing.TB, s string) {
	t.Helper()
	var got AttrList
	m, finished := metered(4*scanFactor*len(s)+scanSlack, func(m *meter) { got = parseAttrs(s, m) })
	if !finished {
		t.Fatalf("ParseAttrs(%q) looked at more than %d bytes", s, m.budget)
	}
	if again := ParseAttrs(s); !slices.Equal(again, got) {
		t.Fatalf("ParseAttrs(%q) = %q, and then %q", s, got, again)
	}
	if want := scanFactor*len(s) + scanSlack; m.scanned > want {
		t.Errorf("ParseAttrs(%q) looked at %d bytes, want at most %d", s, m.scanned, want)
	}
	if !utf8.ValidString(s) && len(got) > 0 {
		t.Errorf("ParseAttrs(%q) = %q, want no attributes for a text that is not UTF-8", s, got)
	}
	seen := map[string]bool{}
	for _, a := range got {
		if a.Name == "" {
			continue
		}
		if seen[a.Name] || a.Name == "0" || strings.ContainsFunc(a.Name, func(r rune) bool { return r > 127 || !isNameByte(byte(r)) || 'A' <= r && r <= 'Z' }) {
			t.Fatalf("ParseAttrs(%q) = %q, which has a name that is wrong or repeated", s, got)
		}
		seen[a.Name] = true
	}
	if len(s) <= plainAttrsLimit {
		if want := referenceAttrs(s); !slices.Equal(got, want) {
			t.Errorf("ParseAttrs(%q) = %q, and the plain search made %q", s, got, want)
		}
	}
}

func fuzzAttrSeeds() []string {
	seeds := []string{`a="b"`, `a='b' c=d "e"`, `a=\x41`, "a=\xc2\xa0b", "\xff", `a="<b"`, `a=`, `"`, `'`, `\`, `0=1`}
	for _, tt := range parseAttrsTests {
		seeds = append(seeds, tt.in)
	}
	return seeds
}

func FuzzParseAttrs(f *testing.F) {
	for _, s := range fuzzAttrSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { checkParseAttrs(t, s) })
}

// checkAutoembedURLs checks what holds for any text.
func checkAutoembedURLs(t testing.TB, s string) {
	t.Helper()
	var got []URLSpan
	m, finished := metered(4*scanFactor*len(s)+scanSlack, func(m *meter) { got = autoembedURLs(s, m) })
	if !finished {
		t.Fatalf("AutoembedURLs(%q) looked at more than %d bytes", s, m.budget)
	}
	if again := AutoembedURLs(s); !slices.Equal(again, got) {
		t.Fatalf("AutoembedURLs(%q) = %v, and then %v", s, got, again)
	}
	if want := scanFactor*len(s) + scanSlack; m.scanned > want {
		t.Errorf("AutoembedURLs(%q) looked at %d bytes, want at most %d", s, m.scanned, want)
	}
	end := 0
	for _, span := range got {
		if span.Start < end || span.End <= span.Start || span.End > len(s) {
			t.Fatalf("AutoembedURLs(%q) = %v, which has a wrong range", s, got)
		}
		end = span.End
		n := urlScheme(s[span.Start:span.End])
		if n == 0 || span.End-span.Start <= n || strings.ContainsFunc(s[span.Start:span.End], func(r rune) bool { return r < 128 && !isURLByte(byte(r)) }) {
			t.Fatalf("AutoembedURLs(%q) = %v, which has the range %q that is not a URL", s, got, s[span.Start:span.End])
		}
	}
	if len(s) <= plainEmbedLimit {
		if want := referenceAutoembed(s); !slices.Equal(got, want) {
			t.Errorf("AutoembedURLs(%q) = %v, and the plain search made %v", s, got, want)
		}
	}
}

func fuzzEmbedSeeds() []string {
	seeds := []string{"http://a", "<p>http://a</p>", "<p \nhttp://a</p>", "<p title=\"\nhttp://u</p>\">", "<!-- <p>http://u</p> -->", "<", "<p", "http://", "\nhttp://a\n"}
	for _, tt := range autoembedTests {
		seeds = append(seeds, tt.in)
	}
	return seeds
}

func FuzzAutoembedURLs(f *testing.F) {
	for _, s := range fuzzEmbedSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { checkAutoembedURLs(t, s) })
}

func TestShortcodeDocuments(t *testing.T) {
	for _, s := range fuzzShortcodeSeeds() {
		for _, rule := range shortcodeRules {
			checkShortcodes(t, s, rule.isName)
		}
	}
	for _, s := range shortcodeDocuments() {
		for _, rule := range shortcodeRules {
			checkShortcodes(t, s, rule.isName)
		}
	}
}

func TestAttrsDocuments(t *testing.T) {
	for _, s := range fuzzAttrSeeds() {
		checkParseAttrs(t, s)
	}
	for _, s := range attrsDocuments() {
		checkParseAttrs(t, s)
	}
}

func TestAutoembedDocuments(t *testing.T) {
	for _, s := range fuzzEmbedSeeds() {
		checkAutoembedURLs(t, s)
	}
	for _, s := range embedDocuments() {
		checkAutoembedURLs(t, s)
	}
}
