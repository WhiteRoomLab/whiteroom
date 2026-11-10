package wpformat

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests compare the package with the PHP of WordPress 7.1.4. They run when WHITEROOM_WP_FORMATTING names a directory
// with the three files below, and php is in PATH. The files are not in the repository.
const envWPFormatting = "WHITEROOM_WP_FORMATTING"

var wpFormattingSums = map[string]string{
	"formatting.php":     "350f6b32303915df970bb0e3ea360fcd54bb0b8a7ecc3d504c3d860ab9468d32",
	"shortcodes.php":     "8b41aed4d1f2dd9a0b517bf85b941bacf4616ae148bff0a7cdf48286df6e7854",
	"class-wp-embed.php": "a8359656527313e546b0201afffb29fee092813ebbf557cd3d75b0e0f1a3c18d",
}

// phpReader reads documents (base64, in a JSON array) from the standard input, and writes a line of JSON for each. The mode
// is the first argument, and the directory of the files of WordPress is the second. The modes are:
//   - autop: the line has the base64 of what wpautop makes of the document.
//   - shortcodes: the third argument is the names of the shortcodes to register (separated by commas). The line has the
//     base64 of what do_shortcode makes of the document when each shortcode makes {{SC|name|attributes|content}}, where the
//     attributes are the pairs of the base64 of the key and of the value (key:value, separated by commas), and the content is
//     in base64 too. WordPress encodes the brackets in elements before it looks for shortcodes. The reader decodes them in
//     the text of the attributes, which it reads again with shortcode_parse_atts, and in the content.
//   - attrs: the line has the pairs of what shortcode_parse_atts makes of the document.
//   - autoembed: the line has the base64 of what autoembed makes of the document when each URL it tries is made {{URL}}.
//
// The line also tells whether PHP reported anything while it worked: a regular expression that fails (for a limit of PHP)
// makes the text that follows empty, and PHP says so. In the modes other than autop, the PHP notice for an attribute list
// that is not UTF-8 is not a failure: the list is empty then, which is what ParseAttrs makes.
const phpReader = `<?php
$last_match = null;
function apply_filters($hook, $value, ...$args) {
	global $last_match;
	if ($hook === 'pre_do_shortcode_tag') {
		$last_match = $args[2];
	}
	return $value;
}
function add_filter(...$args) { return true; }
function remove_filter(...$args) { return true; }
function add_action(...$args) { return true; }
function has_filter(...$args) { return false; }
function _deprecated_function(...$args) {}
function wp_kses_attr_parse($element) { return false; }

$mode = $argv[1];
require $argv[2] . '/formatting.php';
require $argv[2] . '/shortcodes.php';
require $argv[2] . '/class-wp-embed.php';

class MarkEmbed extends WP_Embed {
	public function shortcode($attr, $url = '') { return '{{' . $url . '}}'; }
}

function decode_brackets($text) { return strtr($text, ['&#91;' => '[', '&#93;' => ']']); }
function attr_pairs($atts) {
	$pairs = [];
	foreach ($atts as $key => $value) {
		$pairs[] = base64_encode((string) $key) . ':' . base64_encode($value);
	}
	return implode(',', $pairs);
}

$errors = [];
set_error_handler(function ($number, $message) use (&$errors, $mode) {
	if ($mode !== 'autop' && $number === E_DEPRECATED && str_starts_with($message, 'preg_match_all(): Passing null')) {
		return true;
	}
	$errors[] = $message;
	return true;
});
if ($mode === 'shortcodes') {
	foreach (explode(',', $argv[3]) as $name) {
		add_shortcode($name, function ($atts, $content, $tag) {
			global $last_match;
			$atts = shortcode_parse_atts(decode_brackets($last_match[3]));
			return '{{SC|' . $tag . '|' . attr_pairs($atts) . '|' . base64_encode(decode_brackets((string) $content)) . '}}';
		});
	}
}
if ($mode === 'autoembed') {
	$embed = new MarkEmbed();
}
foreach (json_decode(stream_get_contents(STDIN), true) as $encoded) {
	$doc = base64_decode($encoded, true);
	$errors = [];
	$result = [];
	if ($mode === 'autop') {
		$result['text'] = base64_encode(wpautop($doc));
	} elseif ($mode === 'shortcodes') {
		$result['text'] = base64_encode(do_shortcode($doc, true));
	} elseif ($mode === 'attrs') {
		$result['pairs'] = attr_pairs(shortcode_parse_atts($doc));
	} elseif ($mode === 'autoembed') {
		$result['text'] = base64_encode($embed->autoembed($doc));
	}
	$result['failed'] = count($errors) > 0 || preg_last_error() !== PREG_NO_ERROR;
	echo json_encode($result), "\n";
}
`

type phpFormatted struct {
	Text   string `json:"text"`
	Pairs  string `json:"pairs"`
	Failed bool   `json:"failed"`
}

// wordPressPHP returns the path of php, and the directory of the files of WordPress, or skips the test.
func wordPressPHP(t *testing.T) (php, dir string) {
	t.Helper()
	dir = os.Getenv(envWPFormatting)
	if dir == "" {
		t.Skipf("%s is not set to the directory of the PHP files of WordPress 7.1.4", envWPFormatting)
	}
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not in PATH")
	}
	for name, want := range wpFormattingSums {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", envWPFormatting, err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			t.Fatalf("%s in %s has the SHA-256 %x, but WordPress 7.1.4 has %s: it is another version", name, dir, sum, want)
		}
	}
	return php, dir
}

// runPHP runs phpReader in the mode, with the PHP files in dir, on the documents, and returns the lines it writes. The
// arguments after the documents are given to the reader after the directory.
func runPHP(t *testing.T, php, dir, mode string, docs []string, args ...string) [][]byte {
	t.Helper()
	encoded := make([]string, len(docs))
	for i, doc := range docs {
		encoded[i] = base64.StdEncoding.EncodeToString([]byte(doc))
	}
	input, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "reader.php")
	if err := os.WriteFile(script, []byte(phpReader), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(php, append([]string{"-d", "display_errors=stderr", "-d", "memory_limit=-1", script, mode, dir}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("php: %v\n%s", err, stderr.String())
	}
	if stderr.Len() > 0 {
		t.Logf("php wrote to the standard error:\n%.2000s", stderr.String())
	}
	var lines [][]byte
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(nil, 1<<28)
	for sc.Scan() {
		lines = append(lines, bytes.Clone(sc.Bytes()))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(docs) {
		t.Fatalf("php wrote %d lines for %d documents", len(lines), len(docs))
	}
	return lines
}

// Pieces of the documents that the tests make. Each list is a kind of piece, and a document is made of the pieces of one to
// three kinds, so that what a step of wpautop looks for is often in the text.
var autopPieces = [][]string{
	{
		"a", "b c", "text", "日本語", "x y z", " ", "  ", "\t", "\n", "\n\n", "\n\n\n", " \n", "\n ", " \n ", "\r", "\r\n",
		"\v", "\f", "\xc2\xa0", "&nbsp;", "\n\v\n", "\f\n", "\n\f\n", "\n \n", "\r\n\r\n", "\r\r\n", "\x00", "\x01", "\x7f",
		"\xc2\x85", "\xe3\x80\x80",
	},
	{
		"<p>", "</p>", "<p class=\"a\">", "<div>", "</div>", "<div class='b'>", "<ul>", "</ul>", "<ol>", "</ol>", "<li>",
		"</li>", "<h2>", "</h2>", "<h1 id=x>", "</h1>", "<table>", "</table>", "<tr>", "</tr>", "<td>", "</td>", "<th>",
		"</th>", "<tbody>", "</tbody>", "<blockquote>", "</blockquote>", "<form>", "</form>", "<address>", "</address>",
		"<section>", "</section>", "<figure>", "</figure>", "<hr>", "<hr/>", "<hr />", "<hr  / >", "<hr class=\"a\">", "<dl>",
		"</dl>", "<dt>", "<dd>", "</dd>", "<nav>", "<details>", "<summary>", "</summary>", "<menu>", "<map>", "<area>",
		"<caption>", "<col>", "<colgroup>", "<legend>", "<fieldset>", "<style>", "<hgroup>", "<header>", "<footer>",
		"<article>", "<aside>", "<p/>", "<p\n>", "<div\n\nclass=\"a\">", "<h7>", "<hr\n>", "</div >", "</ div>",
	},
	{
		"<b>", "</b>", "<a href=\"x\">", "</a>", "<span>", "</span>", "<img src=\"a.png\">", "<br>", "<br/>", "<br />",
		"<br >", "<BR>", "<br/ >", "<br  />", "<br\n/>", "<br\n>", "<em>", "<p-x>", "<param>", "<track>", "<thx>", "<link>",
		"<picture>", "<progress>", "<tdx>", "<lix>", "<d>", "<ULx>", "<DIV>", "<P>", "</P>",
	},
	{"<pre>", "</pre>", "<pre class=\"a\">", "<prefix>", "<pre", "</pre", "<pre>\n\n</pre>", "<pre>a\nb</pre>", "<PRE>"},
	{
		"<!-- c -->", "<!-- c\nd -->", "<!--more-->", "<!-->", "<!--->", "<!-- x --", "<![CDATA[x]]>", "<![CDATA[x\ny]]>",
		"<![CDATA[x", "<!", "<", ">", "</", "<!--", "-->", "<!DOCTYPE html>", "<?php x ?>", "<!-", "<![", "<![CDATA[]]>",
	},
	{
		"<option>", "</option>", "<select>", "</select>", "<object>", "</object>", "<param name=\"a\">", "<embed src=\"a\">",
		"</embed>", "<audio>", "</audio>", "<video controls>", "</video>", "<source src=\"a\">", "<track src=\"a\">",
		"[audio]", "[/audio]", "[video]", "[/video]", "<figcaption>", "</figcaption>", "<audio", "</audio]", "[audio>",
		"<option", "<object", "<figcaption", "[video src=\"a\"]", "<video\n>",
	},
	{
		"<script>", "</script>", "<style>", "</style>", "<svg>", "</svg>", "<math>", "</math>", "var a = 1;\nvar b = 2;",
		"<script src=\"a\">", "<scripts>", "<style\n>", "</svg >", "<script",
	},
	{
		"<blockquote cite=\"a>b\">", "<blockquote cite='a>b'>", "<blockquote cite=\"a>", "<blockquote cite='a>",
		"<P><BLOCKQUOTE>", "<p><blockquote>", "<P>", "<BLOCKQUOTE>", "</BLOCKQUOTE>", "\"", "'", "a\"b", "<p><li>", "<p><li",
		"<p><div>", "<p>\n<div>", "<p><blockquote class=\"x\" data='y>'>", "<blockquote\n>", "</blockquote></p>", "<p> <li>",
	},
	{"[gallery]", "[caption]", "[/caption]", "[[x]]", "[x a=\"b\"]", "<WPPreserveNewline />", "<!-- wpnl -->", " <!-- wpnl --> "},
}

func pickFrom(r *rand.Rand, xs []string) string { return xs[r.IntN(len(xs))] }

// piecesDocument returns a document made of pieces of one to three kinds.
func piecesDocument(r *rand.Rand) string {
	kinds := []int{0}
	for range 1 + r.IntN(3) {
		kinds = append(kinds, r.IntN(len(autopPieces)))
	}
	separators := []string{"", "", "", " ", "\n", "\n\n", "\r\n"}
	var sb strings.Builder
	for range 1 + r.IntN(30) {
		sb.WriteString(pickFrom(r, autopPieces[kinds[r.IntN(len(kinds))]]))
		sb.WriteString(pickFrom(r, separators))
	}
	return sb.String()
}

// treeDocument returns text in the shape that people write: blocks that hold text, lists, and inline elements, with
// the white space between them in many ways.
func treeDocument(r *rand.Rand, depth int) string {
	blocks := []string{"p", "div", "blockquote", "ul", "ol", "li", "table", "tr", "td", "h2", "figure", "pre", "section", "dl", "dd"}
	inlines := []string{"b", "a href=\"x\"", "span", "em", "code"}
	spaces := []string{"", "", " ", "\n", "\n\n", "\n\n\n", " \n", "\r\n", "\r\n\r\n", "\t", "\n \n"}
	var sb strings.Builder
	for range 1 + r.IntN(4) {
		sb.WriteString(pickFrom(r, spaces))
		switch k := r.IntN(10); {
		case k < 4 || depth > 3:
			sb.WriteString(pickFrom(r, []string{"text", "two words", "日本語の文", "a\nb", "a<br>b", "a<br />\nb", "a <br/> b", "x<hr>y"}))
			if r.IntN(3) == 0 {
				tag := pickFrom(r, inlines)
				sb.WriteString("<" + tag + ">" + pickFrom(r, []string{"in", "i\nn"}) + "</" + strings.Fields(tag)[0] + ">")
			}
		case k < 9:
			name := pickFrom(r, blocks)
			attrs := pickFrom(r, []string{"", "", "", " class=\"c\"", " title='a>b'", "\n"})
			sb.WriteString("<" + name + attrs + ">")
			sb.WriteString(treeDocument(r, depth+1))
			sb.WriteString("</" + name + ">")
		default:
			sb.WriteString(pickFrom(r, []string{"<!--more-->", "<!-- note\ntext -->", "[gallery]", "<hr>", "<br><br>", "<img src=\"a\">"}))
		}
	}
	return sb.String()
}

// autopDocuments returns the inputs of the tests, and documents made of pieces and trees from a fixed seed.
func autopDocuments() []string {
	var docs []string
	for _, tt := range autopTests {
		docs = append(docs, tt.in)
	}
	r := rand.New(rand.NewPCG(53, 1))
	for range 3000 {
		docs = append(docs, piecesDocument(r))
	}
	for range 1500 {
		docs = append(docs, treeDocument(r, 0))
	}
	// The texts of the tests of the scan, made short, so that PHP does not take long for them.
	for _, tt := range autopInputs {
		docs = append(docs, tt.make(60), tt.make(2000))
	}
	return docs
}

func TestWordPressAutop(t *testing.T) {
	php, dir := wordPressPHP(t)
	docs := autopDocuments()
	lines := runPHP(t, php, dir, "autop", docs)
	compared := 0
	skipped := map[string]int{}
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
		case strings.Contains(doc, "wp-pre-tag-"):
			skipped["the text of a placeholder is in it"]++
		case res.Failed:
			skipped["PHP reported a failure"]++
		default:
			compared++
			if got := Autop(doc); got != string(want) {
				t.Errorf("Autop(%q) = %q, and WordPress made %q", doc, got, want)
			}
		}
	}
	t.Logf("compared %d of %d documents; left out: %v", compared, len(docs), skipped)
	if compared < 3000 {
		t.Errorf("compared only %d documents, want at least 3000", compared)
	}
}
