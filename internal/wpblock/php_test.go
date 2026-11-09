package wpblock

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests compare the package with the PHP of WordPress 7.1.4. They run when WHITEROOM_WP_PARSER names a directory
// with the three files of WP_Block_Parser, and php is in PATH. The files are not in the repository.
const envWPParser = "WHITEROOM_WP_PARSER"

var wpParserSums = map[string]string{
	"class-wp-block-parser.php":       "4f1e15dee25379475cad4464a43e9e4713a6e267dcd7a561509e7d69c482ce3a",
	"class-wp-block-parser-block.php": "7e21f483cf0c9deddd2144e026842bf906c72983908f7bd163c0716e8f6f325e",
	"class-wp-block-parser-frame.php": "3a66c88fa2397a842884908d83d1d03e5cb9cfb23fbb6d3cdf573bcd1520f0ae",
}

// phpReader reads documents (base64, in a JSON array) from the standard input, and writes a line of JSON for each. In
// the mode "json" it is what json_decode makes of the document, and in the mode "parser" it is what WP_Block_Parser
// makes of it. The members of an array are written as pairs, so that their order is kept, and a PHP array that is a
// list is not told from one that is not.
const phpReader = `<?php
$mode = $argv[1];

function norm($v) {
	if (is_float($v) && !is_finite($v)) {
		return ['t' => 'inf'];
	}
	if (is_array($v)) {
		$members = [];
		foreach ($v as $k => $x) {
			$members[] = [(string) $k, norm($x)];
		}
		return ['t' => 'a', 'm' => $members];
	}
	return $v;
}

function shape($b) {
	$inner = [];
	foreach ($b['innerBlocks'] as $x) {
		$inner[] = shape($x);
	}
	return [
		'name' => $b['blockName'],
		'attrs' => norm($b['attrs']),
		'inner' => $inner,
		'html' => $b['innerHTML'],
		'content' => $b['innerContent'],
	];
}

if ($mode === 'parser') {
	require $argv[2] . '/class-wp-block-parser.php';
}
$flags = JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRESERVE_ZERO_FRACTION;
foreach (json_decode(stream_get_contents(STDIN), true) as $encoded) {
	$doc = base64_decode($encoded, true);
	if ($mode === 'json') {
		$v = json_decode($doc, true);
		$result = ['ok' => json_last_error() === JSON_ERROR_NONE, 'v' => norm($v)];
	} else {
		$blocks = (new WP_Block_Parser())->parse($doc);
		$result = ['pcre' => preg_last_error() !== PREG_NO_ERROR, 'blocks' => array_map('shape', $blocks)];
	}
	$line = json_encode($result, $flags, 1 << 20);
	echo ($line === false ? '{"unencodable":true,"ok":' . (isset($result['ok']) && $result['ok'] ? 'true' : 'false') . '}' : $line), "\n";
}
`

type phpBlock struct {
	Name    *string    `json:"name"`
	Attrs   any        `json:"attrs"`
	Inner   []phpBlock `json:"inner"`
	HTML    string     `json:"html"`
	Content []*string  `json:"content"`
}

type phpParsed struct {
	Unencodable bool       `json:"unencodable"`
	PCRE        bool       `json:"pcre"`
	Blocks      []phpBlock `json:"blocks"`
}

type phpDecoded struct {
	Unencodable bool `json:"unencodable"`
	OK          bool `json:"ok"`
	V           any  `json:"v"`
}

// wordPressPHP returns the path of php, and the directory of the files of WP_Block_Parser, or skips the test.
func wordPressPHP(t *testing.T) (php, dir string) {
	t.Helper()
	dir = os.Getenv(envWPParser)
	if dir == "" {
		t.Skipf("%s is not set to the directory of the PHP files of WP_Block_Parser of WordPress 7.1.4", envWPParser)
	}
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not in PATH")
	}
	for name, want := range wpParserSums {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", envWPParser, err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			t.Fatalf("%s in %s has the SHA-256 %x, but WordPress 7.1.4 has %s: it is another version", name, dir, sum, want)
		}
	}
	return php, dir
}

// runPHP runs phpReader in the mode, with the PHP files in dir, on the documents, and returns the lines it writes.
func runPHP(t *testing.T, php, dir, mode string, docs []string) [][]byte {
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
	cmd := exec.Command(php, "-d", "display_errors=stderr", "-d", "memory_limit=-1", script, mode, dir)
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

// phpValue is v as the reader writes what json_decode makes: a number is a float64, and a PHP array of a JSON array or
// object is the pairs of its keys and values.
func phpValue(v Value) any {
	switch v.Kind {
	case Null:
		return nil
	case Bool:
		return v.Bool
	case Number:
		f, _ := strconv.ParseFloat(v.Number, 64)
		if math.IsInf(f, 0) {
			return map[string]any{"t": "inf"}
		}
		return f
	case String:
		return v.String
	case Array:
		members := make([]any, 0, len(v.Array))
		for i, e := range v.Array {
			members = append(members, []any{strconv.Itoa(i), phpValue(e)})
		}
		return map[string]any{"t": "a", "m": members}
	case Object:
		members := make([]any, 0, len(v.Object))
		for _, m := range v.Object {
			members = append(members, []any{m.Name, phpValue(m.Value)})
		}
		return map[string]any{"t": "a", "m": members}
	}
	return nil
}

func phpAttrs(v *Value) any {
	if v == nil {
		return nil
	}
	return phpValue(*v)
}

// hasZeroPiece reports whether a piece of HTML is "0" in b, or in a block inside it. WordPress 7.1 leaves out such a piece
// where it leaves out the empty ones, and the package does not.
func hasZeroPiece(b *Block) bool {
	for _, p := range b.Content {
		if p.Inner < 0 && p.HTML == "0" {
			return true
		}
	}
	for i := range b.Inner {
		if hasZeroPiece(&b.Inner[i]) {
			return true
		}
	}
	return false
}

// openTwice reports whether the document ends in two blocks that are open, one inside the other. WordPress 7.1 reads
// those as blocks one after another, with the HTML of the inner block in the outer block too.
func openTwice(bs []Block) bool {
	for i := range bs {
		if n := len(bs[i].Inner); bs[i].Unclosed && n > 0 && bs[i].Inner[n-1].Unclosed {
			return true
		}
	}
	return false
}

// differ returns how the blocks differ from what WordPress made, or "".
func differ(path string, got []Block, want []phpBlock) string {
	if len(got) != len(want) {
		return fmt.Sprintf("%s: %d blocks, and WordPress made %d", path, len(got), len(want))
	}
	for i := range got {
		g, w := &got[i], &want[i]
		at := fmt.Sprintf("%s[%d]", path, i)
		name := ""
		if w.Name != nil {
			name = *w.Name
		}
		if g.Name != name {
			return fmt.Sprintf("%s: the name is %q, and WordPress made %q", at, g.Name, name)
		}
		if attrs := phpAttrs(g.Attrs); !reflect.DeepEqual(attrs, w.Attrs) {
			return fmt.Sprintf("%s: the attributes are %s, and WordPress made %v", at, jsonText(g.Attrs), w.Attrs)
		}
		if html := g.HTML(); html != w.HTML {
			return fmt.Sprintf("%s: innerHTML is %q, and WordPress made %q", at, html, w.HTML)
		}
		var gotContent, wantContent []string
		for _, p := range g.Content {
			if p.Inner < 0 {
				gotContent = append(gotContent, "html "+p.HTML)
			} else {
				gotContent = append(gotContent, "inner")
			}
		}
		for _, p := range w.Content {
			switch {
			case p == nil:
				wantContent = append(wantContent, "inner")
			// WordPress 7.1 keeps an empty piece before the closing delimiter of an inner block; the package, like 7.2, does not.
			case *p != "":
				wantContent = append(wantContent, "html "+*p)
			}
		}
		if !reflect.DeepEqual(gotContent, wantContent) {
			return fmt.Sprintf("%s: innerContent is %q, and WordPress made %q", at, gotContent, wantContent)
		}
		if msg := differ(at+".inner", g.Inner, w.Inner); msg != "" {
			return msg
		}
	}
	return ""
}

// parserDocuments returns the documents to read: the ones of the tests, documents made of pieces, trees of blocks that
// are spoiled a little, and a few with attributes that nest deep.
func parserDocuments() []string {
	var docs []string
	for _, tt := range parseTests {
		docs = append(docs, tt.doc)
	}
	r := rand.New(rand.NewPCG(52, 2))
	for range 1500 {
		docs = append(docs, soupDocument(r))
	}
	trees := rand.New(rand.NewPCG(52, 3))
	for range 1500 {
		var sb strings.Builder
		writeItems(&sb, genItems(trees, 0))
		docs = append(docs, mutate(trees, sb.String(), treePieces))
	}
	for _, levels := range []int{509, 510, 511, 512, 513, 600} {
		docs = append(docs,
			"<!-- wp:a "+nestObjects(levels, "1")+" -->t<!-- /wp:a -->",
			"<!-- wp:a {\"a\":"+strings.Repeat("[", levels-1)+strings.Repeat("]", levels-1)+"} /-->",
		)
	}
	return docs
}

func TestWordPressParser(t *testing.T) {
	php, dir := wordPressPHP(t)
	docs := parserDocuments()
	lines := runPHP(t, php, dir, "parser", docs)
	compared := 0
	skipped := map[string]int{}
	for i, doc := range docs {
		var res phpParsed
		if err := json.Unmarshal(lines[i], &res); err != nil {
			t.Fatalf("the line for %q is not JSON: %v\n%s", doc, err, lines[i])
		}
		got, err := Parse(doc, Options{})
		if err != nil {
			t.Fatalf("Parse(%q) = %v", doc, err)
		}
		if msg := problem(doc, got); msg != "" {
			t.Errorf("Parse(%q): %s", doc, msg)
		}
		zero := false
		for j := range got {
			zero = zero || hasZeroPiece(&got[j])
		}
		switch {
		case res.Unencodable:
			skipped["WordPress made output that is not JSON"]++
		case res.PCRE:
			skipped["the regular expression of WordPress failed"]++
		case openTwice(got):
			skipped["the document ends in two blocks that are open"]++
		case zero:
			skipped["a piece of HTML is 0"]++
		default:
			compared++
			if msg := differ("", got, res.Blocks); msg != "" {
				t.Errorf("Parse(%q): %s\nParse:\n%sWordPress:\n%.3000s", doc, msg, wantsText(shapes(doc, got)), lines[i])
			}
		}
	}
	t.Logf("compared %d of %d documents; left out: %v", compared, len(docs), skipped)
	if compared < 2000 {
		t.Errorf("compared only %d documents, want at least 2000", compared)
	}
}

// jsonDocuments returns the texts to read as JSON: the ones of the tests, and the same changed a little.
func jsonDocuments() []string {
	var docs []string
	for _, tt := range decodeTests {
		docs = append(docs, tt.in)
	}
	docs = append(docs, soupAttrs...)
	seeds := slices.Clone(docs)
	r := rand.New(rand.NewPCG(52, 4))
	pieces := []string{
		`"`, `\`, `{`, `}`, `[`, `]`, `,`, `:`, "\x00", "\x01", "\x1f", " ", "\t", "\n", "\r", "\f", "\v", "\x7f", "\x80", "\xc3", "\xff",
		"0", "1", "9", "-", "+", ".", "e", "E", "t", "r", "u", "f", "a", "l", "s", "n", "x", `\` + `u`, `\` + `ud800`, `\` + `udc00`,
		`\n`, `\/`, `\"`, "true", "false", "null", "TRUE", "NaN", "1e999", "-0", "00", "[]", "{}", `""`, "//", "/*",
	}
	for range 4000 {
		docs = append(docs, mutate(r, seeds[r.IntN(len(seeds))], pieces))
	}
	return docs
}

func TestWordPressJSON(t *testing.T) {
	php, dir := wordPressPHP(t)
	docs := jsonDocuments()
	lines := runPHP(t, php, dir, "json", docs)
	valid, compared := 0, 0
	for i, doc := range docs {
		var res phpDecoded
		if err := json.Unmarshal(lines[i], &res); err != nil {
			t.Fatalf("the line for %q is not JSON: %v\n%s", doc, err, lines[i])
		}
		v, ok := DecodeAttrs(doc)
		if ok != res.OK {
			t.Errorf("DecodeAttrs(%q) is valid: %v; json_decode says %v", doc, ok, res.OK)
			continue
		}
		if !ok {
			continue
		}
		valid++
		if res.Unencodable {
			continue
		}
		compared++
		if got := phpValue(*v); !reflect.DeepEqual(got, res.V) {
			t.Errorf("DecodeAttrs(%q) = %s, and json_decode made %v", doc, jsonText(v), res.V)
		}
	}
	t.Logf("%d texts, %d of them valid, and the values of %d compared", len(docs), valid, compared)
	if valid < 300 || valid > len(docs)-300 {
		t.Errorf("%d of %d texts are valid, want a mix of valid and not", valid, len(docs))
	}
}
