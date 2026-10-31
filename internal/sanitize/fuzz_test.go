package sanitize

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/html"
)

// fuzzSeeds are inputs for FuzzSanitize besides those of the tests: markup that the parser handles in special ways,
// and some of the inputs of internal/html/fuzz_test.go that take the parser many steps.
var fuzzSeeds = []string{
	allowedLegacy,
	allowedHTML,
	"<p>a<plaintext>b</p><table>c",
	"<script><!--<script></script>x",
	"<pre>\n\na</pre><textarea>\n\nb</textarea><listing>\nc</listing>",
	"<?a b\x00c?>\x00<svg>\x00</svg>",
	`<form id="outer"><div></form><form id="inner"><input>`,
	"<mAth><mteXt><tABle><mglYph>",
	"<select><option><svg><style><img src=x onerror=alert(1)></style>",
	"<a href=\"javas&#99;ript:x\" title='a\"b'>&lt;&amp;&gt;&#0;</a>",
	`<b id="x"><b><b><b>y</b></b></b>z</b>`,
	`<p><b id=x><i id=y>a</p>b<p>c<h1 id=y>d`,
	`<a href="/a" id="a"><table><a href="/b" id="a">x</a></table></a>`,
	`<p class="a b-0paragr1 B-0PARAGR1"><a target="_BLANK" rel="nofollow external">x</a></p>`,
	`<table><tr><td>a</td></tr><script>x</script><font>y</font></table>`,
	`<p><b>bold<i>both</b>italic`,
	`<h1 id="t">a</h1><h1><h1>b</h1></h1>`,
	"[gallery ids=\"1,2\"]<p>[caption]text[/caption]</p>",
	strings.Repeat("<div>", 255) + "<p><b><button><div>x</div></button></b></p>",
	strings.Repeat("<div>", 254) + "<b><i><u><p>x<button><div>y",
	strings.Repeat("<div>", 300) + strings.Repeat("</p>", 300),
	strings.Repeat("<font a=1><font a=2><font a=3><font a=4>", 30) + strings.Repeat("</p>x", 100),
	"<b " + strings.Repeat("a", 64) + "=1>" + strings.Repeat("<p>x</p>", 100),
	"<svg>" + strings.Repeat("<g>", 300) + strings.Repeat("</x>", 300),
	strings.Repeat("<table><tr><td>", 100) + strings.Repeat("<form>", 100),
	"<b>" + strings.Repeat("<span>", 100) + "<div>" + strings.Repeat("<div>", 100) + "</b>",
	strings.Repeat("<template>", 100) + strings.Repeat("<a><table><a>", 30),
	strings.Repeat("<math><mi><svg><foreignObject><p>", 50) + strings.Repeat("</x></p>", 50),
}

// FuzzSanitize sanitizes s as the HTML kind if htmlKind, else as Legacy, with nil Options.URL and Options.ID, and
// checks that:
//   - Sanitize does not panic, and returns HTML, or ErrLimit with no HTML and no changes; never ErrUnstable, which
//     would mean that parsing and cleaning its output does not settle within maxPasses times.
//   - The HTML is valid UTF-8 without U+0000, and a fixed point: sanitizing it again gives it, with no changes.
//   - The HTML passes checkPolicy: parsed as browsers parse innerHTML, it gives a tree that gives the HTML again, so
//     browsers build the tree that the checks see; the tree has only text, and the elements and attributes that the
//     kind keeps, with URLs of the kinds that each attribute allows, valid and unique ids, no reserved classes, target
//     only _blank and then rel starting with noopener; no comments, processing instructions or doctypes, no content in
//     iframe elements, and no element nested more than maxDepth deep; and in the HTML, each "<" starts a tag, and text
//     and attribute values are escaped (markupProblem).
//
// It then sanitizes s again with an Options.ID that keeps the ids of even length and an Options.URL that numbers the
// URLs in https URLs, and checks that the HTML is the first with the other ids removed and the URLs replaced, and
// passes checkPolicy: ID was called for the ids of the first HTML in document order, the tree did not change, and the
// final pass of Sanitize kept the HTML stable. That run can return ErrLimit instead: the removed ids shorten the
// HTML, and the limits of the parse that checks it.
func FuzzSanitize(f *testing.F) {
	for _, tests := range [][]sanitizeTest{attacks, stableTests} {
		for _, tt := range tests {
			f.Add(tt.in, false)
			f.Add(tt.in, true)
		}
	}
	for _, tt := range mediaAttacks {
		f.Add(tt.in, false)
		f.Add(tt.in, true)
	}
	for _, s := range fuzzSeeds {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, s string, htmlKind bool) {
		kind := Legacy
		if htmlKind {
			kind = HTML
		}
		out, changes, err := Sanitize(s, Options{Kind: kind})
		if err != nil {
			if !errors.Is(err, ErrLimit) || out != "" || changes != nil {
				t.Fatalf("Sanitize(%+q) as kind %d: %+q, %d changes and the error %v", s, kind, out, len(changes), err)
			}
			return
		}
		if !utf8.ValidString(out) || strings.IndexByte(out, 0) >= 0 {
			t.Errorf("Sanitize(%+q) as kind %d = %+q, which is not valid UTF-8 or has U+0000", s, kind, out)
		}
		name := strconv.QuoteToASCII(s)
		checkPolicy(t, name, kind, out)
		if again, changes, err := Sanitize(out, Options{Kind: kind}); again != out || changes != nil || err != nil {
			t.Errorf("Sanitize(%+q) as kind %d = %+q, which gives %+q with the changes %q and the error %v", s, kind,
				out, again, formatChanges(changes), err)
		}

		var calls []string
		urls := 0
		got, _, err := Sanitize(s, Options{
			Kind: kind,
			ID: func(id string) bool {
				calls = append(calls, id)
				return len(id)%2 == 0
			},
			URL: func(string) string {
				urls++
				return "https://u.example/" + strconv.Itoa(urls)
			},
		})
		if errors.Is(err, ErrLimit) {
			return
		}
		if err != nil {
			t.Fatalf("Sanitize(%+q) as kind %d with ID and URL: %v", s, kind, err)
		}
		want, ids := finalPass(t, out)
		if got != want || !slices.Equal(calls, ids) {
			t.Errorf("Sanitize(%+q) as kind %d with ID and URL = %+q with the calls of ID %q, want %+q and %q", s, kind,
				got, calls, want, ids)
		}
		checkPolicy(t, name+" with ID and URL", kind, got)
	})
}

// finalPass returns out, HTML that Sanitize wrote with nil Options.URL and Options.ID, without its ids of odd length
// and with its URLs numbered as FuzzSanitize numbers them, and the ids of out in document order.
func finalPass(t *testing.T, out string) (string, []string) {
	t.Helper()
	opts, _ := limits(len(out))
	nodes, err := html.ParseFragment(out, &html.Node{Type: html.ElementNode, Name: "div"}, opts)
	if err != nil {
		t.Fatalf("parsing %+q: %v", out, err)
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	var ids []string
	urls := 0
	for n := range descendants(root) {
		var attrs []html.Attr
		for _, a := range n.Attrs {
			if a.Name == "id" {
				if ids = append(ids, a.Value); len(a.Value)%2 != 0 {
					continue
				}
			}
			if urlAttrs[[2]string{n.Name, a.Name}] != noURL {
				urls++
				a.Value = "https://u.example/" + strconv.Itoa(urls)
			}
			attrs = append(attrs, a)
		}
		n.Attrs = attrs
	}
	return html.Render(root), ids
}
