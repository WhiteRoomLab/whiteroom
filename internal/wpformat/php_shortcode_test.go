package wpformat

import (
	"encoding/base64"
	"encoding/json/v2"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// phpShortcodeNames are the shortcodes that the PHP reader registers: names that are a prefix of other names, names with
// "_" and "-", and the names that the importer will know.
var phpShortcodeNames = []string{
	"a", "a1", "b", "caption", "contact-form", "contact-form-7", "embed", "fo", "foo", "foo-bar", "foo_x", "gallery", "x-y",
}

func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// wordPressTagNames returns the names that do_shortcode finds before it makes the expression for the shortcodes: the run of
// bytes after each "[" that has none of the bytes of the class of the expression. It registers only the names in the run.
func wordPressTagNames(doc string) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < len(doc); i++ {
		if doc[i] != '[' {
			continue
		}
		j := i + 1
		for j < len(doc) && doc[j] > ' ' && !strings.ContainsRune("<>&/[]=", rune(doc[j])) {
			j++
		}
		if j > i+1 {
			names[doc[i+1:j]] = true
		}
	}
	return names
}

// phpIntegerKey reports whether PHP makes the string a key that is a number.
func phpIntegerKey(name string) bool {
	n, err := strconv.ParseInt(name, 10, 64)
	return err == nil && strconv.FormatInt(n, 10) == name
}

// phpPairs writes the attributes in the way of the PHP reader. The key of a value that has no name is its number among them.
func phpPairs(attrs AttrList) (pairs string, integerName bool) {
	var parts []string
	unnamed := 0
	for _, a := range attrs {
		key := a.Name
		if key == "" {
			key = strconv.Itoa(unnamed)
			unnamed++
		} else if phpIntegerKey(key) {
			integerName = true
		}
		parts = append(parts, base64Of(key)+":"+base64Of(a.Value))
	}
	return strings.Join(parts, ","), integerName
}

// shortcodeText is what the PHP reader makes of doc, made with FindShortcodes and ParseAttrs.
func shortcodeText(doc string) (text string, integerName bool) {
	present := wordPressTagNames(doc)
	if !slices.ContainsFunc(phpShortcodeNames, func(name string) bool { return present[name] }) {
		// WordPress leaves the text as it is, even the entities that it changes otherwise.
		return doc, false
	}
	doc = strings.NewReplacer("&#91;", "&#091;", "&#93;", "&#093;").Replace(doc)
	isName := func(name string, _ byte) bool { return slices.Contains(phpShortcodeNames, name) && present[name] }
	var b strings.Builder
	pos := 0
	for _, c := range FindShortcodes(doc, isName) {
		b.WriteString(doc[pos:c.Start])
		pos = c.End
		if c.Open && c.Close {
			b.WriteString(doc[c.Start+1 : c.End-1])
			continue
		}
		if c.Open {
			b.WriteByte('[')
		}
		pairs, integer := phpPairs(ParseAttrs(c.Attrs))
		integerName = integerName || integer
		b.WriteString("{{SC|" + c.Name + "|" + pairs + "|" + base64Of(c.Content) + "}}")
		if c.Close {
			b.WriteByte(']')
		}
	}
	b.WriteString(doc[pos:])
	return b.String(), integerName
}

// embedText is what the PHP reader makes of doc, made with AutoembedURLs.
func embedText(doc string) string {
	var b strings.Builder
	pos := 0
	for _, span := range AutoembedURLs(doc) {
		b.WriteString(doc[pos:span.Start])
		b.WriteString("{{" + doc[span.Start:span.End] + "}}")
		pos = span.End
	}
	b.WriteString(doc[pos:])
	return b.String()
}

var shortcodePieces = [][]string{
	{
		"[", "]", "[[", "]]", "[[[", "]]]", "[/", "/]", "/", "=", "[foo", "[foo]", "[/foo]", "[foo/]", "[foo /]", "[[foo]]",
		"[[foo]", "[foo]]", "[foo-bar]", "[/foo-bar]", "[foo_x]", "[/foo_x]", "[fooX]", "[Foo]", "[foo.x]", "[foo=1]", "[ foo]",
		"[a]", "[/a]", "[a1]", "[b]", "[/b]", "[x]", "[/x]", "[fo]", "[/fo]", "[foo-]", "[-foo]", "[x-y]", "[/x-y]", "[foo\n]",
		"[foo\t]", "[foo\"1\"]", "[foo'1']", "[foo&amp;]", "[foo<b>]", "[foo/", "[/foo", "[/foo ]", "[ /foo]", "[foo]x[/foo]",
		"[caption]", "[/caption]", "[gallery]", "[embed]", "[/embed]", "[contact-form-7]", "[contact-form]",
	},
	{
		`[foo a="1"]`, `[foo a='1']`, `[foo a=1 b="2" 'c' "d" e]`, `[foo a="x]y"]`, `[foo a=/]`, `[foo a="1"/]`, `[foo /a]`,
		"[foo\ta=1\n]", `[caption id="x" align="left"]`, `[gallery ids="1,2,3" columns=2]`, `[gallery size=thumbnail/]`,
		`[contact-form-7 id="abc" title="T"]`, `[contact-form 12 "T"]`, `[foo a="<b>"]`, `[foo a="<b"]`, `[foo a=\x41\n]`,
		`[foo A=1 a=2]`, `[foo 0=1 a=2]`, `[foo 1=1 "a"]`, `[foo a="x"b="y"]`, `[foo a=" "]`, "[foo a=\"\xc2\xa0\"]",
		"[foo\xc2\xa0a=1]", "[foo a=1\xc2\xa0b=2]", "[foo \xff]", `[foo "" '' a=1]`, `[foo a=b"c d]`, `[a b c d e f g h i j k l m n o p q r s]`,
	},
	{
		"x", " ", "  ", "\n", "\n\n", "\r\n", "日本語", "a/b", "&amp;", "&#91;", "&#93;", "&#091;", "&#x5B;", "\"", "'", "\\", ">", "<",
		"]]]", "[[[", "-", "_", "0", "text [foo] text", "[", "]",
	},
	{
		"<b>", "</b>", "<br/>", "<br />", `<a href="x">`, "</a>", "<p>", "</p>", `<div class="[foo]">`, `<span title='[/foo]'>`,
		`<img alt="]">`, "<!-- [foo] -->", "<!-- [foo]x[/foo] -->", "<![CDATA[ [foo] ]]>", "<!--", "-->", "<![CDATA[", "]]>", "<a",
		`<a href="[`, "<p [foo]>", "<x [/foo]>", "<a\n>", "<>", "</>", `<b title="]">`, `<b title="[">`, "<i data-x='[foo]'>",
		"<p [foo>", "<p ]>", "<!-- ] -->", "<!-- [ -->", "<![CDATA[ ] ]]>",
	},
}

func shortcodeDocument(r *rand.Rand) string {
	kinds := []int{0}
	for range 1 + r.IntN(3) {
		kinds = append(kinds, r.IntN(len(shortcodePieces)))
	}
	separators := []string{"", "", "", " ", "\n"}
	var sb strings.Builder
	for range 1 + r.IntN(24) {
		sb.WriteString(pickFrom(r, shortcodePieces[kinds[r.IntN(len(kinds))]]))
		sb.WriteString(pickFrom(r, separators))
	}
	return sb.String()
}

// shortcodeTree returns shortcodes that hold shortcodes, text, and elements.
func shortcodeTree(r *rand.Rand, depth int) string {
	names := []string{"foo", "foo-bar", "caption", "gallery", "a", "x-y", "unknown"}
	var sb strings.Builder
	for range 1 + r.IntN(4) {
		switch k := r.IntN(10); {
		case k < 3 || depth > 3:
			sb.WriteString(pickFrom(r, []string{"text", "two words", "日本語", "a\nb", "<b>in</b>", "<br>", `<a href="[x]">l</a>`, "[ ]", "]"}))
		case k < 8:
			name := pickFrom(r, names)
			attrs := pickFrom(r, []string{"", "", ` a="1"`, " b=2 c", ` d='3'`, "\n", " /", ` e="<b>"`})
			sb.WriteString("[" + name + attrs + "]")
			sb.WriteString(shortcodeTree(r, depth+1))
			if r.IntN(5) != 0 {
				sb.WriteString("[/" + pickFrom(r, []string{name, name, name, "foo"}) + "]")
			}
		case k < 9:
			sb.WriteString(pickFrom(r, []string{"[[", "]]", "[[foo]]", "[foo/]", "[foo]]"}))
		default:
			sb.WriteString(pickFrom(r, []string{"<p>", "</p>", "<!-- [foo] -->", "<div title=\"[/foo]\">", "</div>"}))
		}
	}
	return sb.String()
}

func shortcodeDocuments() []string {
	var docs []string
	for _, tt := range findShortcodeTests {
		docs = append(docs, tt.in)
	}
	r := rand.New(rand.NewPCG(53, 2))
	for range 3000 {
		docs = append(docs, shortcodeDocument(r))
	}
	for range 1500 {
		docs = append(docs, shortcodeTree(r, 0))
	}
	for _, unit := range []string{"[foo", "[foo]", "[/foo]", "[[", "<", "[foo]<b title=\"]\">", "[foo]<b>", "[a][/b]", "]"} {
		docs = append(docs, rep(unit, 60), rep(unit, 2000))
	}
	docs = append(docs, "[foo "+strings.Repeat("a", 2000), "[foo "+strings.Repeat("/", 2000)+"]")
	return docs
}

// compareShortcodes runs the documents through PHP and FindShortcodes, and returns how many it compared, and why it left out
// the others.
func compareShortcodes(t *testing.T, php, dir string, docs []string) (compared int, skipped map[string]int) {
	t.Helper()
	lines := runPHP(t, php, dir, "shortcodes", docs, strings.Join(phpShortcodeNames, ","))
	skipped = map[string]int{}
	for i, doc := range docs {
		var res phpFormatted
		if err := json.Unmarshal(lines[i], &res); err != nil {
			t.Fatalf("the line for %q is not JSON: %v\n%s", doc, err, lines[i])
		}
		want, err := base64.StdEncoding.DecodeString(res.Text)
		if err != nil {
			t.Fatalf("the text for %q is not base64: %v", doc, err)
		}
		got, integerName := shortcodeText(doc)
		switch {
		case res.Failed:
			skipped["PHP reported a failure"]++
		case integerName:
			skipped["an attribute has a name that is a number"]++
		default:
			compared++
			if got != string(want) {
				t.Errorf("the shortcodes of %q: got %q, and WordPress made %q", doc, got, want)
			}
		}
	}
	return compared, skipped
}

func TestWordPressShortcodes(t *testing.T) {
	php, dir := wordPressPHP(t)
	docs := shortcodeDocuments()
	compared, skipped := compareShortcodes(t, php, dir, docs)
	t.Logf("compared %d of %d documents; left out: %v", compared, len(docs), skipped)
	if compared < 2000 {
		t.Errorf("compared only %d documents, want at least 2000", compared)
	}
}

var attrPieces = [][]string{
	{"a", "b", "ab", "A", "B", "Cd", "x-y", "_z", "1", "0", "12", "-5", "00", "9a", "id", "title"},
	{"=", " = ", "= ", " =", "==", `="`, "='", "=x", `="x"`, "='y'", `="x y"`, "='y z'", `=""`, "=''", "=b=c", `=x"y`},
	{" ", "  ", "\t", "\n", "\r", "\x0b", "\x0c", "\xc2\xa0", "\xe2\x80\x8b", "\xc2\xa0\xe2\x80\x8b", "\xc2\xa0 "},
	{`"`, "'", `"q"`, "'r'", `"s t"`, "'u v'", `""`, "''", `"it's"`, `'say "hi"'`},
	{`\`, `\\`, `\n`, `\t`, `\x41`, `\x4`, `\xg`, `\x`, `\101`, `\1018`, `\777`, `\q`, `\8`, `\0`, `\"`, `\'`, `\a\b\f\r\v`, `\x3c`, `\x3e`},
	{"<", ">", "<b>", "</b>", "<b", "<<", "<>", "a<b>c", "x>y", "<b <i>", "<i>x</i><u"},
	{"\xff", "\xc3", "é", "日本語", "\xed\xa0\x80", "\xf0\x9f\x98\x80"},
}

func attrsDocument(r *rand.Rand) string {
	kinds := []int{0, 1}
	for range 1 + r.IntN(3) {
		kinds = append(kinds, r.IntN(len(attrPieces)))
	}
	separators := []string{"", "", " ", " ", "\n"}
	var sb strings.Builder
	for range 1 + r.IntN(14) {
		sb.WriteString(pickFrom(r, attrPieces[kinds[r.IntN(len(kinds))]]))
		sb.WriteString(pickFrom(r, separators))
	}
	return sb.String()
}

func attrsDocuments() []string {
	var docs []string
	for _, tt := range parseAttrsTests {
		docs = append(docs, tt.in)
	}
	r := rand.New(rand.NewPCG(53, 3))
	for range 4000 {
		docs = append(docs, attrsDocument(r))
	}
	for _, unit := range []string{`a="`, "a=", "a ", `"`, "'", `\`, "<", "a=b ", "a  =  ", `a="x" `, "\xc2\xa0", "a='"} {
		docs = append(docs, rep(unit, 60), rep(unit, 2000))
	}
	return docs
}

// compareAttrs runs the documents through PHP and ParseAttrs.
func compareAttrs(t *testing.T, php, dir string, docs []string) (compared int, skipped map[string]int) {
	t.Helper()
	lines := runPHP(t, php, dir, "attrs", docs)
	skipped = map[string]int{}
	for i, doc := range docs {
		var res phpFormatted
		if err := json.Unmarshal(lines[i], &res); err != nil {
			t.Fatalf("the line for %q is not JSON: %v\n%s", doc, err, lines[i])
		}
		got, integerName := phpPairs(ParseAttrs(doc))
		switch {
		case res.Failed:
			skipped["PHP reported a failure"]++
		case integerName:
			skipped["a name is a number"]++
		default:
			compared++
			if got != res.Pairs {
				t.Errorf("the attributes of %q: got %q, and WordPress made %q", doc, got, res.Pairs)
			}
		}
	}
	return compared, skipped
}

func TestWordPressShortcodeAttrs(t *testing.T) {
	php, dir := wordPressPHP(t)
	docs := attrsDocuments()
	compared, skipped := compareAttrs(t, php, dir, docs)
	t.Logf("compared %d of %d documents; left out: %v", compared, len(docs), skipped)
	if compared < 2000 {
		t.Errorf("compared only %d documents, want at least 2000", compared)
	}
}

var embedPieces = [][]string{
	{
		"http://a", "https://b.example/c?d=1&amp;e=2", "HTTP://X", "HtTpS://y/z", "http", "http:/", "http://", "https://", "ftp://x",
		"xhttp://q", "http://a/b c", `http://a"b`, "http://a<b>", "http://a>b", "https://youtu.be/x", "http://a.b/c-d_e",
	},
	{" ", "  ", "\t", "\n", "\n\n", "\r\n", "\r", "\x0b", "\x0c", "\xc2\xa0", "\x00", "text", "日本語", ">", "\n \n"},
	{
		"<p>", "</p>", "<P>", "</P>", `<p class="a">`, "<p >", "<p\n>", "<p/>", "<p\tx>", "<pre>", "</pre>", "<div>", "</div>",
		`<a href="http://x">`, "</a>", "<br>", "<b>", "</b>", `<p title="`, `">`, "<p x=\"\n", "<p\n", "<p ", "<p  >", "<p x>",
		"</p >", "</p", "<pp>", "<p>x",
	},
	{"<!-- ", " -->", "<!--", "<![CDATA[", "]]>", "<", ">", `"`, "'", "-->", "<!-- x\ny -->", "<![CDATA[x\ny]]>", "<!-->", "<!--->"},
}

func embedDocument(r *rand.Rand) string {
	kinds := []int{0, 1}
	for range 1 + r.IntN(3) {
		kinds = append(kinds, r.IntN(len(embedPieces)))
	}
	separators := []string{"", "", "", " ", "\n", "\n\n"}
	var sb strings.Builder
	for range 1 + r.IntN(20) {
		sb.WriteString(pickFrom(r, embedPieces[kinds[r.IntN(len(kinds))]]))
		sb.WriteString(pickFrom(r, separators))
	}
	return sb.String()
}

func embedDocuments() []string {
	var docs []string
	for _, tt := range autoembedTests {
		docs = append(docs, tt.in)
	}
	r := rand.New(rand.NewPCG(53, 4))
	for range 4000 {
		docs = append(docs, embedDocument(r))
	}
	for _, unit := range []string{"http://a\n", "http://a b\n", "<p>", "<p>http://a</p>", "<p http://a", "<p \n", "<p x", "http://a  ", "\nhttp://a", "<p>\nhttp://a"} {
		docs = append(docs, rep(unit, 60), rep(unit, 2000))
	}
	return docs
}

// compareAutoembed runs the documents through PHP and AutoembedURLs.
func compareAutoembed(t *testing.T, php, dir string, docs []string) (compared int, skipped map[string]int) {
	t.Helper()
	lines := runPHP(t, php, dir, "autoembed", docs)
	skipped = map[string]int{}
	for i, doc := range docs {
		var res phpFormatted
		if err := json.Unmarshal(lines[i], &res); err != nil {
			t.Fatalf("the line for %q is not JSON: %v\n%s", doc, err, lines[i])
		}
		want, err := base64.StdEncoding.DecodeString(res.Text)
		if err != nil {
			t.Fatalf("the text for %q is not base64: %v", doc, err)
		}
		switch {
		case strings.Contains(doc, "wp-line-break") || strings.Contains(doc, "{{"):
			skipped["the text of a placeholder is in it"]++
		case res.Failed:
			skipped["PHP reported a failure"]++
		default:
			compared++
			if got := embedText(doc); got != string(want) {
				t.Errorf("the URLs of %q: got %q, and WordPress made %q", doc, got, want)
			}
		}
	}
	return compared, skipped
}

func TestWordPressAutoembed(t *testing.T) {
	php, dir := wordPressPHP(t)
	docs := embedDocuments()
	compared, skipped := compareAutoembed(t, php, dir, docs)
	t.Logf("compared %d of %d documents; left out: %v", compared, len(docs), skipped)
	if compared < 2000 {
		t.Errorf("compared only %d documents, want at least 2000", compared)
	}
}
