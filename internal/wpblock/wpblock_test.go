package wpblock

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// want is the expected shape of a block, with the delimiters as text, so that the positions need not be counted.
type want struct {
	name   string // empty for freeform HTML
	attrs  string // as jsonText writes them
	opener string
	closer string
	flags  string // "void", "unclosed", or "stray"
	parts  []any  // the HTML (string) and the inner blocks (want) of Content
}

func blk(name, attrs, opener, closer string, parts ...any) want {
	return want{name: name, attrs: attrs, opener: opener, closer: closer, parts: parts}
}

func voidBlk(name, attrs, opener string) want {
	return want{name: name, attrs: attrs, opener: opener, flags: "void"}
}

func openBlk(name, attrs, opener string, parts ...any) want {
	return want{name: name, attrs: attrs, opener: opener, flags: "unclosed", parts: parts}
}

func free(html string) want {
	return want{attrs: "{}", parts: []any{html}}
}

func stray(html string) want {
	w := free(html)
	w.flags = "stray"
	return w
}

func (w want) String() string {
	var sb strings.Builder
	w.write(&sb, "")
	return sb.String()
}

func (w want) write(sb *strings.Builder, indent string) {
	if w.name == "" {
		fmt.Fprintf(sb, "%sfreeform %s %q\n", indent, w.flags, w.parts)
		return
	}
	fmt.Fprintf(sb, "%s%s %s %s open=%q close=%q\n", indent, w.name, w.attrs, w.flags, w.opener, w.closer)
	for _, p := range w.parts {
		switch p := p.(type) {
		case string:
			fmt.Fprintf(sb, "%s  html %q\n", indent, p)
		case want:
			p.write(sb, indent+"  ")
		}
	}
}

func wantsText(ws []want) string {
	var sb strings.Builder
	for _, w := range ws {
		w.write(&sb, "")
	}
	return sb.String()
}

func shapes(doc string, bs []Block) []want {
	var ws []want
	for i := range bs {
		ws = append(ws, shape(doc, &bs[i]))
	}
	return ws
}

func shape(doc string, b *Block) want {
	w := want{name: b.Name, attrs: jsonText(b.Attrs), opener: doc[b.Start:b.OpenerEnd], closer: doc[b.CloserStart:b.End]}
	var flags []string
	if b.Void {
		flags = append(flags, "void")
	}
	if b.Unclosed {
		flags = append(flags, "unclosed")
	}
	if b.Stray {
		flags = append(flags, "stray")
	}
	w.flags = strings.Join(flags, ",")
	for _, p := range b.Content {
		if p.Inner < 0 {
			w.parts = append(w.parts, p.HTML)
		} else {
			w.parts = append(w.parts, shape(doc, &b.Inner[p.Inner]))
		}
	}
	return w
}

// jsonText writes v as JSON in one form: no white space, and only the escapes that are needed.
func jsonText(v *Value) string {
	if v == nil {
		return "null"
	}
	var sb strings.Builder
	writeJSON(&sb, *v)
	return sb.String()
}

func writeJSON(sb *strings.Builder, v Value) {
	switch v.Kind {
	case Null:
		sb.WriteString("null")
	case Bool:
		sb.WriteString(strconv.FormatBool(v.Bool))
	case Number:
		sb.WriteString(v.Number)
	case String:
		writeQuoted(sb, v.String)
	case Array:
		sb.WriteByte('[')
		for i, e := range v.Array {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeJSON(sb, e)
		}
		sb.WriteByte(']')
	case Object:
		sb.WriteByte('{')
		for i, m := range v.Object {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeQuoted(sb, m.Name)
			sb.WriteByte(':')
			writeJSON(sb, m.Value)
		}
		sb.WriteByte('}')
	}
}

func writeQuoted(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c < ' ':
			sb.WriteString("\\" + "u00")
			sb.WriteByte("0123456789abcdef"[c>>4])
			sb.WriteByte("0123456789abcdef"[c&15])
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
}

// problem returns what is wrong with the blocks that Parse returned for doc, or "" if they are as they must be.
func problem(doc string, bs []Block) string {
	pos := 0
	for i := range bs {
		b := &bs[i]
		if b.Start != pos {
			return fmt.Sprintf("block %d starts at %d, but the one before it ends at %d", i, b.Start, pos)
		}
		if msg := blockProblem(doc, b); msg != "" {
			return fmt.Sprintf("block %d: %s", i, msg)
		}
		if b.Stray && (i != len(bs)-1 || b.Name != "") {
			return fmt.Sprintf("block %d is stray, but it is not the last, or has a name", i)
		}
		pos = b.End
	}
	if pos != len(doc) {
		return fmt.Sprintf("the blocks end at %d, but the document at %d", pos, len(doc))
	}
	return ""
}

func blockProblem(doc string, b *Block) string {
	if b.Start < 0 || b.Start > b.OpenerEnd || b.OpenerEnd > b.CloserStart || b.CloserStart > b.End || b.End > len(doc) {
		return fmt.Sprintf("the positions %d %d %d %d are not in order in a document of %d", b.Start, b.OpenerEnd, b.CloserStart, b.End, len(doc))
	}
	if b.Name == "" {
		switch {
		case b.Attrs == nil || b.Attrs.Kind != Object || len(b.Attrs.Object) != 0:
			return fmt.Sprintf("freeform HTML has the attributes %s", jsonText(b.Attrs))
		case len(b.Content) != 1 || b.Content[0].Inner != -1 || b.Content[0].HTML == "" || b.Content[0].HTML != doc[b.Start:b.End]:
			return fmt.Sprintf("freeform HTML has the content %+v, but the document has %q", b.Content, doc[b.Start:b.End])
		case b.OpenerEnd != b.Start || b.CloserStart != b.End || len(b.Inner) != 0 || b.Void || b.Unclosed:
			return "freeform HTML has delimiters, inner blocks, or a flag"
		}
		return ""
	}
	if b.Stray {
		return "a block with a name is stray"
	}
	if i := strings.IndexByte(b.Name, '/'); i < 1 || strings.Contains(b.Name[i+1:], "/") || i == len(b.Name)-1 {
		return fmt.Sprintf("the name %q has no namespace and name", b.Name)
	}
	if b.Attrs != nil && b.Attrs.Kind != Object {
		return fmt.Sprintf("the attributes %s are not an object", jsonText(b.Attrs))
	}
	if opener := doc[b.Start:b.OpenerEnd]; !strings.HasPrefix(opener, "<!--") || !strings.HasSuffix(opener, "-->") {
		return fmt.Sprintf("the opening delimiter is %q", opener)
	}
	if b.Void {
		if len(b.Content) != 0 || len(b.Inner) != 0 || b.OpenerEnd != b.End || b.Unclosed {
			return "a void block has content, inner blocks, or a closing delimiter, or is unclosed"
		}
		return ""
	}
	if b.Unclosed {
		if b.CloserStart != b.End || b.End != len(doc) {
			return "an unclosed block does not end with the document"
		}
	} else if closer := doc[b.CloserStart:b.End]; !strings.HasPrefix(closer, "<!--") || !strings.HasSuffix(closer, "-->") {
		return fmt.Sprintf("the closing delimiter is %q", closer)
	}
	cursor, next, wasHTML := b.OpenerEnd, 0, false
	var html strings.Builder
	for _, p := range b.Content {
		if p.Inner < 0 {
			if p.HTML == "" || wasHTML || !strings.HasPrefix(doc[cursor:], p.HTML) {
				return fmt.Sprintf("the HTML piece %q is empty, follows another, or is not at %d", p.HTML, cursor)
			}
			cursor += len(p.HTML)
			html.WriteString(p.HTML)
			wasHTML = true
			continue
		}
		wasHTML = false
		if p.Inner != next || p.Inner >= len(b.Inner) || p.HTML != "" {
			return fmt.Sprintf("the place of an inner block is %d, want %d of %d", p.Inner, next, len(b.Inner))
		}
		in := &b.Inner[p.Inner]
		if in.Name == "" || in.Start != cursor {
			return fmt.Sprintf("the inner block %d has the name %q, and starts at %d, not %d", p.Inner, in.Name, in.Start, cursor)
		}
		if msg := blockProblem(doc, in); msg != "" {
			return fmt.Sprintf("inner block %d: %s", p.Inner, msg)
		}
		if in.Unclosed && (!b.Unclosed || p.Inner != len(b.Inner)-1) {
			return "an inner block is unclosed, but it is not the last one in an unclosed block"
		}
		cursor = in.End
		next++
	}
	if next != len(b.Inner) || cursor != b.CloserStart {
		return fmt.Sprintf("the content has %d of %d inner blocks, and ends at %d, not %d", next, len(b.Inner), cursor, b.CloserStart)
	}
	if got := b.HTML(); got != html.String() {
		return fmt.Sprintf("HTML() is %q, but the pieces of HTML make %q", got, html.String())
	}
	return ""
}

const (
	pOpen  = "<!-- wp:paragraph -->"
	pClose = "<!-- /wp:paragraph -->"
)

func para(attrs, html string) want {
	return blk("core/paragraph", attrs, pOpen, pClose, html)
}

var parseTests = []struct {
	name string
	doc  string
	want []want
}{
	{"empty", "", nil},
	{"HTML", "<p>a</p>", []want{free("<p>a</p>")}},
	{"white space", " \n", []want{free(" \n")}},
	{"paragraph", pOpen + "\n<p>a</p>\n" + pClose, []want{para("{}", "\n<p>a</p>\n")}},
	{"HTML around", "x" + pOpen + "y" + pClose + "z", []want{free("x"), para("{}", "y"), free("z")}},
	{"two blocks", pOpen + "a" + pClose + "\n\n" + pOpen + "b" + pClose, []want{para("{}", "a"), free("\n\n"), para("{}", "b")}},
	{"nothing inside", "<!-- wp:a --><!-- /wp:a -->", []want{blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:a -->")}},
	{"delimiters back to back", "<!-- wp:a /--><!-- wp:b /-->", []want{voidBlk("core/a", "{}", "<!-- wp:a /-->"), voidBlk("core/b", "{}", "<!-- wp:b /-->")}},

	// Names.
	{"namespace", "<!-- wp:my-plugin/box -->b<!-- /wp:my-plugin/box -->", []want{
		blk("my-plugin/box", "{}", "<!-- wp:my-plugin/box -->", "<!-- /wp:my-plugin/box -->", "b"),
	}},
	{"core namespace", "<!-- wp:core/paragraph -->p<!-- /wp:core/paragraph -->", []want{
		blk("core/paragraph", "{}", "<!-- wp:core/paragraph -->", "<!-- /wp:core/paragraph -->", "p"),
	}},
	{"name characters", "<!-- wp:a1_b-c --><!-- /wp:a1_b-c -->", []want{
		blk("core/a1_b-c", "{}", "<!-- wp:a1_b-c -->", "<!-- /wp:a1_b-c -->"),
	}},
	{"namespace characters", "<!-- wp:x-1_y/z -->t<!-- /wp:x-1_y/z -->", []want{
		blk("x-1_y/z", "{}", "<!-- wp:x-1_y/z -->", "<!-- /wp:x-1_y/z -->", "t"),
	}},
	{"closer of another namespace", "<!-- wp:a -->t<!-- /wp:core/a -->", []want{
		blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:core/a -->", "t"),
	}},

	// White space in a delimiter.
	{"tab", "<!--\twp:a\t-->", []want{openBlk("core/a", "{}", "<!--\twp:a\t-->")}},
	{"LF", "<!--\nwp:a\n-->", []want{openBlk("core/a", "{}", "<!--\nwp:a\n-->")}},
	{"CR", "<!--\rwp:a\r-->", []want{openBlk("core/a", "{}", "<!--\rwp:a\r-->")}},
	{"FF", "<!--\fwp:a\f-->", []want{openBlk("core/a", "{}", "<!--\fwp:a\f-->")}},
	{"much white space", "<!--  \t wp:a \n\r\f -->", []want{openBlk("core/a", "{}", "<!--  \t wp:a \n\r\f -->")}},
	{"white space in a closer", "<!-- wp:a -->t<!--\t/wp:a\n-->", []want{blk("core/a", "{}", "<!-- wp:a -->", "<!--\t/wp:a\n-->", "t")}},
	{"white space in a void block", "<!--\nwp:a\t/-->", []want{voidBlk("core/a", "{}", "<!--\nwp:a\t/-->")}},

	// Attributes.
	{"attributes", "<!-- wp:a {\"level\":2} -->t<!-- /wp:a -->", []want{
		blk("core/a", `{"level":2}`, `<!-- wp:a {"level":2} -->`, "<!-- /wp:a -->", "t"),
	}},
	{"no members", "<!-- wp:a {} -->t<!-- /wp:a -->", []want{blk("core/a", "{}", "<!-- wp:a {} -->", "<!-- /wp:a -->", "t")}},
	{"nested attributes", `<!-- wp:a {"a":{"b":[1,2.50,{"c":null}],"d":true},"e":"f"} -->`, []want{
		openBlk("core/a", `{"a":{"b":[1,2.50,{"c":null}],"d":true},"e":"f"}`, `<!-- wp:a {"a":{"b":[1,2.50,{"c":null}],"d":true},"e":"f"} -->`),
	}},
	{"braces in strings", `<!-- wp:a {"x":"{ }","y":"}"} -->`, []want{
		openBlk("core/a", `{"x":"{ }","y":"}"}`, `<!-- wp:a {"x":"{ }","y":"}"} -->`),
	}},
	{"arrow in a string", `<!-- wp:a {"x":"-->"} -->`, []want{openBlk("core/a", `{"x":"-->"}`, `<!-- wp:a {"x":"-->"} -->`)}},
	{"delimiter in a string", `<!-- wp:a {"x":"<!-- wp:b -->"} -->`, []want{
		openBlk("core/a", `{"x":"<!-- wp:b -->"}`, `<!-- wp:a {"x":"<!-- wp:b -->"} -->`),
	}},
	{"end in a string", `<!-- wp:a {"x":"} -->"} --><!-- /wp:a -->`, []want{
		blk("core/a", "null", `<!-- wp:a {"x":"} -->`, "<!-- /wp:a -->", `"} -->`),
	}},
	{"attributes over lines", "<!-- wp:a {\n  \"x\": 1,\n  \"y\": [\n    2\n  ]\n} -->", []want{
		openBlk("core/a", `{"x":1,"y":[2]}`, "<!-- wp:a {\n  \"x\": 1,\n  \"y\": [\n    2\n  ]\n} -->"),
	}},
	{"braces in a row", `<!-- wp:a {"x":{"y":{"z":1}}} -->`, []want{openBlk("core/a", `{"x":{"y":{"z":1}}}`, `<!-- wp:a {"x":{"y":{"z":1}}} -->`)}},
	{"white space after attributes", "<!-- wp:a {\"x\":1}\t\r\n  -->", []want{openBlk("core/a", `{"x":1}`, "<!-- wp:a {\"x\":1}\t\r\n  -->")}},
	{"FF after attributes", "<!-- wp:a {\"x\":1}\f-->", []want{openBlk("core/a", "null", "<!-- wp:a {\"x\":1}\f-->")}},
	{"duplicate name", `<!-- wp:a {"a":1,"b":2,"a":3} -->`, []want{openBlk("core/a", `{"a":3,"b":2}`, `<!-- wp:a {"a":1,"b":2,"a":3} -->`)}},
	{"not JSON", `<!-- wp:a {"a":} -->t<!-- /wp:a -->`, []want{blk("core/a", "null", `<!-- wp:a {"a":} -->`, "<!-- /wp:a -->", "t")}},
	{"not an object", `<!-- wp:a {"a":1} {"b":2} -->`, []want{openBlk("core/a", "null", `<!-- wp:a {"a":1} {"b":2} -->`)}},
	{"single quotes", `<!-- wp:a {'a':1} -->`, []want{openBlk("core/a", "null", `<!-- wp:a {'a':1} -->`)}},
	{"unpaired surrogate", `<!-- wp:a {"a":"\` + `ud800"} -->`, []want{openBlk("core/a", "null", `<!-- wp:a {"a":"\`+`ud800"} -->`)}},
	{"surrogate pair", `<!-- wp:a {"a":"\` + `ud83d\` + `ude00"} -->`, []want{
		openBlk("core/a", "{\"a\":\""+string(rune(0x1F600))+"\"}", `<!-- wp:a {"a":"\`+`ud83d\`+`ude00"} -->`),
	}},
	{"no space before attributes", `<!-- wp:a{"x":1} -->`, []want{free(`<!-- wp:a{"x":1} -->`)}},
	{"no space after attributes", `<!-- wp:a {"x":1}-->`, []want{free(`<!-- wp:a {"x":1}-->`)}},
	{"attributes swallow the next delimiter", `<!-- wp:a {"x":1}--> t <!-- wp:b {"y":2} -->`, []want{
		openBlk("core/a", "null", `<!-- wp:a {"x":1}--> t <!-- wp:b {"y":2} -->`),
	}},
	{"attributes never end", `<!-- wp:a {"x":1 -->t<!-- /wp:a -->`, []want{stray(`<!-- wp:a {"x":1 -->t<!-- /wp:a -->`)}},
	{"attributes never end, and no closer", `<!-- wp:a {"x":1 -->t`, []want{free(`<!-- wp:a {"x":1 -->t`)}},
	{"valid delimiter after attributes that never end", `<!-- wp:a {"x":1 --><!-- wp:b -->t<!-- /wp:b -->`, []want{
		free(`<!-- wp:a {"x":1 -->`),
		blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:b -->", "t"),
	}},
	{"closer is not a closing brace", `<!-- wp:a {"x":1} / -->`, []want{free(`<!-- wp:a {"x":1} / -->`)}},
	{"closer on a line", "<!-- wp:a {\"x\":1}\n/-->", []want{voidBlk("core/a", `{"x":1}`, "<!-- wp:a {\"x\":1}\n/-->")}},

	// Void blocks.
	{"void", "<!-- wp:a /-->", []want{voidBlk("core/a", "{}", "<!-- wp:a /-->")}},
	{"void with attributes", `<!-- wp:a {"x":1} /-->`, []want{voidBlk("core/a", `{"x":1}`, `<!-- wp:a {"x":1} /-->`)}},
	{"void with attributes and many spaces", "<!-- wp:a {\"x\":1}   /-->", []want{voidBlk("core/a", `{"x":1}`, "<!-- wp:a {\"x\":1}   /-->")}},
	{"void after HTML", "x<!-- wp:a /-->y", []want{free("x"), voidBlk("core/a", "{}", "<!-- wp:a /-->"), free("y")}},
	{"closer that is void", "<!-- /wp:a /-->", []want{voidBlk("core/a", "{}", "<!-- /wp:a /-->")}},
	{"closer that is void, with attributes", `<!-- /wp:a {"x":1} /-->`, []want{voidBlk("core/a", `{"x":1}`, `<!-- /wp:a {"x":1} /-->`)}},
	{"closer with attributes", `<!-- wp:a -->t<!-- /wp:a {"x":1} -->`, []want{
		blk("core/a", "{}", "<!-- wp:a -->", `<!-- /wp:a {"x":1} -->`, "t"),
	}},
	{"closer with attributes that are not JSON", `<!-- wp:a -->t<!-- /wp:a {x} -->`, []want{
		blk("core/a", "{}", "<!-- wp:a -->", `<!-- /wp:a {x} -->`, "t"),
	}},
	{"void inside", "<!-- wp:g -->a<!-- wp:b /-->c<!-- /wp:g -->", []want{
		blk("core/g", "{}", "<!-- wp:g -->", "<!-- /wp:g -->", "a", voidBlk("core/b", "{}", "<!-- wp:b /-->"), "c"),
	}},
	{"void inside, with nothing around", "<!-- wp:g --><!-- wp:b /--><!-- wp:b /--><!-- /wp:g -->", []want{
		blk("core/g", "{}", "<!-- wp:g -->", "<!-- /wp:g -->", voidBlk("core/b", "{}", "<!-- wp:b /-->"), voidBlk("core/b", "{}", "<!-- wp:b /-->")),
	}},

	// Not delimiters.
	{"no space after the opening", "<!--wp:a -->", []want{free("<!--wp:a -->")}},
	{"no space after the name", "<!-- wp:a-->", []want{free("<!-- wp:a-->")}},
	{"no space before a slash", "<!-- wp:a/-->", []want{free("<!-- wp:a/-->")}},
	{"space after the slash", "<!-- wp:a / -->", []want{free("<!-- wp:a / -->")}},
	{"space after the slash of a closer", "<!-- / wp:a -->", []want{free("<!-- / wp:a -->")}},
	{"upper case prefix", "<!-- WP:a -->", []want{free("<!-- WP:a -->")}},
	{"upper case name", "<!-- wp:A -->", []want{free("<!-- wp:A -->")}},
	{"upper case namespace", "<!-- wp:A/b -->", []want{free("<!-- wp:A/b -->")}},
	{"name starts with a digit", "<!-- wp:1a -->", []want{free("<!-- wp:1a -->")}},
	{"name starts with a hyphen", "<!-- wp:-a -->", []want{free("<!-- wp:-a -->")}},
	{"two slashes", "<!-- wp:a/b/c -->", []want{free("<!-- wp:a/b/c -->")}},
	{"no name", "<!-- wp: -->", []want{free("<!-- wp: -->")}},
	{"no name after a namespace", "<!-- wp:a/ -->", []want{free("<!-- wp:a/ -->")}},
	{"slash first", "<!-- wp:/a -->", []want{free("<!-- wp:/a -->")}},
	{"vertical tab before", "<!--\vwp:a -->", []want{free("<!--\vwp:a -->")}},
	{"vertical tab after", "<!-- wp:a\v-->", []want{free("<!-- wp:a\v-->")}},
	{"no space after the colon", "<!-- wp :a -->", []want{free("<!-- wp :a -->")}},
	{"other comment", "<!-- a -->", []want{free("<!-- a -->")}},
	{"end of the document", "<!-- wp:a ", []want{free("<!-- wp:a ")}},
	{"end of the document, after the opening", "x<!--", []want{free("x<!--")}},
	{"almost a closer", "<!-- wp:a --!>", []want{free("<!-- wp:a --!>")}},

	// Nesting.
	{"group", "<!-- wp:group -->\n<div>\n" + pOpen + "\n<p>a</p>\n" + pClose + "\n\n" + pOpen + "\n<p>b</p>\n" + pClose + "\n</div>\n<!-- /wp:group -->", []want{
		blk("core/group", "{}", "<!-- wp:group -->", "<!-- /wp:group -->", "\n<div>\n", para("{}", "\n<p>a</p>\n"), "\n\n", para("{}", "\n<p>b</p>\n"), "\n</div>\n"),
	}},
	{"three levels", "<!-- wp:a -->1<!-- wp:b -->2<!-- wp:c -->3<!-- /wp:c -->4<!-- /wp:b -->5<!-- /wp:a -->6", []want{
		blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:a -->", "1",
			blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:b -->", "2",
				blk("core/c", "{}", "<!-- wp:c -->", "<!-- /wp:c -->", "3"), "4"), "5"),
		free("6"),
	}},
	{"inner blocks one after another", "<!-- wp:a --><!-- wp:b -->1<!-- /wp:b --><!-- wp:c -->2<!-- /wp:c --><!-- /wp:a -->", []want{
		blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:a -->",
			blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:b -->", "1"),
			blk("core/c", "{}", "<!-- wp:c -->", "<!-- /wp:c -->", "2")),
	}},
	{"closer of another name", "<!-- wp:a --><!-- wp:b -->t<!-- /wp:a --><!-- /wp:b -->", []want{
		blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:b -->",
			blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:a -->", "t")),
	}},
	{"0 inside", "<!-- wp:a -->0<!-- /wp:a -->", []want{blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:a -->", "0")}},
	{"0 between blocks", "<!-- wp:g --><!-- wp:a -->x<!-- /wp:a -->0<!-- wp:b /--><!-- /wp:g -->", []want{
		blk("core/g", "{}", "<!-- wp:g -->", "<!-- /wp:g -->",
			blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:a -->", "x"), "0", voidBlk("core/b", "{}", "<!-- wp:b /-->")),
	}},
	{"0 before the closer of an inner block", "<!-- wp:g --><!-- wp:a -->0<!-- /wp:a --><!-- /wp:g -->", []want{
		blk("core/g", "{}", "<!-- wp:g -->", "<!-- /wp:g -->", blk("core/a", "{}", "<!-- wp:a -->", "<!-- /wp:a -->", "0")),
	}},
	{"0 around", "0<!-- wp:a /-->0", []want{free("0"), voidBlk("core/a", "{}", "<!-- wp:a /-->"), free("0")}},
	{"HTML comment around a delimiter", "<!-- a <!-- wp:b --> c -->", []want{free("<!-- a "), openBlk("core/b", "{}", "<!-- wp:b -->", " c -->")}},
	{"delimiter in an HTML comment that is closed", "<!-- <!-- wp:b -->x<!-- /wp:b --> -->", []want{
		free("<!-- "), blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:b -->", "x"), free(" -->"),
	}},
	{"opening of a comment before a delimiter", "<!--<!-- wp:a /-->", []want{free("<!--"), voidBlk("core/a", "{}", "<!-- wp:a /-->")}},
	{"white space between blocks", "<!-- wp:a /-->\n\n<!-- wp:b /-->\n", []want{
		voidBlk("core/a", "{}", "<!-- wp:a /-->"), free("\n\n"), voidBlk("core/b", "{}", "<!-- wp:b /-->"), free("\n"),
	}},

	// Closers with nothing to close.
	{"stray closer", "x<!-- /wp:a -->y<!-- wp:b -->z<!-- /wp:b -->", []want{stray("x<!-- /wp:a -->y<!-- wp:b -->z<!-- /wp:b -->")}},
	{"stray closer after a block", "<!-- wp:a /-->x<!-- /wp:b -->y<!-- wp:c /-->", []want{
		voidBlk("core/a", "{}", "<!-- wp:a /-->"), stray("x<!-- /wp:b -->y<!-- wp:c /-->"),
	}},
	{"stray closer after a closed block", pOpen + "a" + pClose + "<!-- /wp:b -->", []want{para("{}", "a"), stray("<!-- /wp:b -->")}},
	{"two closers", pOpen + "a" + pClose + pClose + "z", []want{para("{}", "a"), stray(pClose + "z")}},

	// Blocks that the document does not close.
	{"unclosed", "<!-- wp:a -->text", []want{openBlk("core/a", "{}", "<!-- wp:a -->", "text")}},
	{"unclosed after HTML", "x<!-- wp:a -->text", []want{free("x"), openBlk("core/a", "{}", "<!-- wp:a -->", "text")}},
	{"unclosed with nothing in it", "x<!-- wp:a -->", []want{free("x"), openBlk("core/a", "{}", "<!-- wp:a -->")}},
	{"unclosed after a block", "<!-- wp:a /-->x<!-- wp:b -->y", []want{voidBlk("core/a", "{}", "<!-- wp:a /-->"), free("x"), openBlk("core/b", "{}", "<!-- wp:b -->", "y")}},
	{"unclosed with a block inside", "<!-- wp:a --><!-- wp:b -->1<!-- /wp:b -->2", []want{
		openBlk("core/a", "{}", "<!-- wp:a -->", blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:b -->", "1"), "2"),
	}},
	{"unclosed twice", "x<!-- wp:a -->1<!-- wp:b -->2", []want{
		free("x"),
		openBlk("core/a", "{}", "<!-- wp:a -->", "1", openBlk("core/b", "{}", "<!-- wp:b -->", "2")),
	}},
	{"unclosed three times", "x<!-- wp:a -->1<!-- wp:b -->2<!-- wp:c -->3", []want{
		free("x"),
		openBlk("core/a", "{}", "<!-- wp:a -->", "1", openBlk("core/b", "{}", "<!-- wp:b -->", "2", openBlk("core/c", "{}", "<!-- wp:c -->", "3"))),
	}},
	{"closer closes the inner block", "<!-- wp:g -->1<!-- wp:a /-->2<!-- wp:b -->3<!-- /wp:g -->4", []want{
		openBlk("core/g", "{}", "<!-- wp:g -->", "1", voidBlk("core/a", "{}", "<!-- wp:a /-->"), "2", blk("core/b", "{}", "<!-- wp:b -->", "<!-- /wp:g -->", "3"), "4"),
	}},
	{"unclosed with HTML before the inner block", "<!-- wp:a -->1<!-- wp:b /-->2<!-- wp:c -->3", []want{
		openBlk("core/a", "{}", "<!-- wp:a -->", "1", voidBlk("core/b", "{}", "<!-- wp:b /-->"), "2", openBlk("core/c", "{}", "<!-- wp:c -->", "3")),
	}},
	{"unclosed one inside another with nothing between", "<!-- wp:a --><!-- wp:b --><!-- wp:c -->", []want{
		openBlk("core/a", "{}", "<!-- wp:a -->", openBlk("core/b", "{}", "<!-- wp:b -->", openBlk("core/c", "{}", "<!-- wp:c -->"))),
	}},
}

func TestParse(t *testing.T) {
	for _, tt := range parseTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.doc, Options{})
			if err != nil {
				t.Fatalf("Parse(%q) = %v", tt.doc, err)
			}
			if msg := problem(tt.doc, got); msg != "" {
				t.Errorf("Parse(%q): %s", tt.doc, msg)
			}
			if gs := shapes(tt.doc, got); !reflect.DeepEqual(gs, tt.want) {
				t.Errorf("Parse(%q) =\n%swant\n%s", tt.doc, wantsText(gs), wantsText(tt.want))
			}
		})
	}
}

func TestPositions(t *testing.T) {
	doc := "x<!-- wp:g -->a<!-- wp:b /-->c<!-- /wp:g -->y"
	got, err := Parse(doc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Parse(%q) = %d blocks, want 3", doc, len(got))
	}
	g := got[1]
	if g.Name != "core/g" || g.Start != 1 || g.OpenerEnd != 14 || g.CloserStart != 30 || g.End != 44 {
		t.Errorf("the group is %q at %d %d %d %d, want core/g at 1 14 30 44", g.Name, g.Start, g.OpenerEnd, g.CloserStart, g.End)
	}
	b := g.Inner[0]
	if b.Start != 15 || b.End != 29 || b.OpenerEnd != 29 || b.CloserStart != 29 || !b.Void {
		t.Errorf("the void block is at %d %d %d %d, want 15 29 29 29", b.Start, b.OpenerEnd, b.CloserStart, b.End)
	}
	if got[0].Start != 0 || got[0].End != 1 || got[2].Start != 44 || got[2].End != len(doc) {
		t.Errorf("the HTML is at %d-%d and %d-%d, want 0-1 and 44-%d", got[0].Start, got[0].End, got[2].Start, got[2].End, len(doc))
	}
}

func TestHTML(t *testing.T) {
	tests := []struct {
		doc  string
		want string
	}{
		{"<!-- wp:a --><!-- /wp:a -->", ""},
		{"<!-- wp:a /-->", ""},
		{"<!-- wp:a -->x<!-- /wp:a -->", "x"},
		{"<!-- wp:g -->1<!-- wp:a -->in<!-- /wp:a -->2<!-- wp:b /-->3<!-- /wp:g -->", "123"},
		{"<!-- wp:g -->1<!-- wp:a -->in", "1"},
		{"<!-- wp:g -->1<!-- wp:a -->in<!-- wp:b -->x", "1"},
	}
	for _, tt := range tests {
		got, err := Parse(tt.doc, Options{})
		if err != nil || len(got) != 1 {
			t.Errorf("Parse(%q) = %d blocks, %v", tt.doc, len(got), err)
			continue
		}
		if html := got[0].HTML(); html != tt.want {
			t.Errorf("Parse(%q): HTML() = %q, want %q", tt.doc, html, tt.want)
		}
	}
	if got, _ := Parse("x", Options{}); got[0].HTML() != "x" {
		t.Errorf("HTML() of freeform HTML = %q, want x", got[0].HTML())
	}
}

func TestMaxBlocks(t *testing.T) {
	block := "<!-- wp:a -->x<!-- /wp:a -->"
	tests := []struct {
		name string
		doc  string
		max  int
		ok   bool
	}{
		{"none", "text", 1, true},
		{"at the limit", block + block, 2, true},
		{"over the limit", block + block + block, 2, false},
		{"void blocks", strings.Repeat("<!-- wp:a /-->", 3), 2, false},
		{"HTML does not count", strings.Repeat("x<!-- wp:a /-->", 2) + "x", 2, true},
		{"inner blocks count", "<!-- wp:g -->" + block + block + "<!-- /wp:g -->", 3, true},
		{"inner blocks over the limit", "<!-- wp:g -->" + block + block + "<!-- /wp:g -->", 2, false},
		{"unclosed blocks count", strings.Repeat("<!-- wp:a -->", 5), 4, false},
		{"closers do not count", strings.Repeat("<!-- wp:a -->", 2) + strings.Repeat("<!-- /wp:a -->", 5), 2, true},
		{"a closer that is void counts", "<!-- /wp:a /--><!-- /wp:a /-->", 1, false},
		{"zero is the default", strings.Repeat("<!-- wp:a /-->", 5), 0, true},
		{"negative is the default", strings.Repeat("<!-- wp:a /-->", 5), -1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.doc, Options{MaxBlocks: tt.max})
			switch {
			case tt.ok && err != nil:
				t.Errorf("Parse(MaxBlocks %d) = %v, want no error", tt.max, err)
			case !tt.ok && !errors.Is(err, ErrLimit):
				t.Errorf("Parse(MaxBlocks %d) = %v, want ErrLimit", tt.max, err)
			case !tt.ok && got != nil:
				t.Errorf("Parse(MaxBlocks %d) = %d blocks with an error, want none", tt.max, len(got))
			}
		})
	}
	doc := strings.Repeat("<!-- wp:a /-->", DefaultMaxBlocks+1)
	if _, err := Parse(doc, Options{}); !errors.Is(err, ErrLimit) {
		t.Errorf("Parse of %d blocks = %v, want ErrLimit", DefaultMaxBlocks+1, err)
	}
	if got, err := Parse(doc[:len(doc)-len("<!-- wp:a /-->")], Options{}); err != nil || len(got) != DefaultMaxBlocks {
		t.Errorf("Parse of %d blocks = %d blocks, %v, want all of them", DefaultMaxBlocks, len(got), err)
	}
}

// A genBlock is a block that genItems made, and the text that wrote it.
type genBlock struct {
	name           string
	opener, closer string
	void           bool
	parts          []any // HTML (string) and *genBlock
	attrs          string
}

var (
	genNames = []struct{ written, name string }{
		{"paragraph", "core/paragraph"}, {"core/image", "core/image"}, {"my-plugin/box", "my-plugin/box"}, {"a", "core/a"},
		{"x_y-1", "core/x_y-1"}, {"a1/b2", "a1/b2"},
	}
	genAttrs = []struct{ text, want string }{
		{"", "{}"}, {"{}", "{}"}, {`{"level":2}`, `{"level":2}`}, {`{"a":{"b":[1,2.50,{"c":null}],"d":true,"e":false}}`, `{"a":{"b":[1,2.50,{"c":null}],"d":true,"e":false}}`},
		{`{"s":"} x"}`, `{"s":"} x"}`}, {`{"s":"-->"}`, `{"s":"-->"}`}, {`{"s":"<!-- wp:z -->","t":"{"}`, `{"s":"<!-- wp:z -->","t":"{"}`},
		{`{"n":[-1,0,1e2,3.25]}`, `{"n":[-1,0,1e2,3.25]}`}, {`{"x":{"y":{"z":{}}}}`, `{"x":{"y":{"z":{}}}}`}, {`{"a":1,"a":2}`, `{"a":2}`},
		{`{"bad":}`, "null"}, {`{"bad":1,}`, "null"},
	}
	genHTML = []string{
		"x", "\n", "<p>a</p>", "text with spaces", `<div class="a">`, "}", "{", "-->", "<!-", "<!", "--", "a<b", "0", " ", "\n\n", "</div>\n",
	}
	genSpaces = []string{" ", " ", " ", "\t", "\n", "\r", "\f", "  ", " \n "}
)

func genItems(r *rand.Rand, depth int) []any {
	var items []any
	wasHTML := false
	for range r.IntN(5) {
		if !wasHTML && r.IntN(2) == 0 {
			items = append(items, genHTML[r.IntN(len(genHTML))])
			wasHTML = true
			continue
		}
		items = append(items, genBlockAt(r, depth))
		wasHTML = false
	}
	return items
}

func genBlockAt(r *rand.Rand, depth int) *genBlock {
	n := genNames[r.IntN(len(genNames))]
	a := genAttrs[r.IntN(len(genAttrs))]
	b := &genBlock{name: n.name, attrs: a.want}
	b.void = depth >= 3 || r.IntN(4) == 0
	if !b.void {
		b.parts = genItems(r, depth+1)
	}
	space := func() string { return genSpaces[r.IntN(len(genSpaces))] }
	opener := "<!--" + space() + "wp:" + n.written + space()
	if a.text != "" {
		after := space()
		opener += a.text + after
		// json_decode does not take an FF as white space after the value.
		if strings.Contains(after, "\f") {
			b.attrs = "null"
		}
	}
	if b.void {
		opener += "/"
	}
	b.opener = opener + "-->"
	if !b.void {
		b.closer = "<!--" + space() + "/wp:" + n.written + space() + "-->"
	}
	return b
}

func (b *genBlock) write(sb *strings.Builder) {
	sb.WriteString(b.opener)
	writeItems(sb, b.parts)
	sb.WriteString(b.closer)
}

func writeItems(sb *strings.Builder, items []any) {
	for _, it := range items {
		switch it := it.(type) {
		case string:
			sb.WriteString(it)
		case *genBlock:
			it.write(sb)
		}
	}
}

func (b *genBlock) want() want {
	w := want{name: b.name, attrs: b.attrs, opener: b.opener, closer: b.closer, parts: wantItems(b.parts)}
	if b.void {
		w.flags = "void"
	}
	return w
}

func wantItems(items []any) []any {
	var parts []any
	for _, it := range items {
		switch it := it.(type) {
		case string:
			parts = append(parts, it)
		case *genBlock:
			parts = append(parts, it.want())
		}
	}
	return parts
}

func TestRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(52, 1))
	for i := range 600 {
		items := genItems(r, 0)
		var sb strings.Builder
		writeItems(&sb, items)
		doc := sb.String()
		var wants []want
		for _, it := range items {
			switch it := it.(type) {
			case string:
				wants = append(wants, free(it))
			case *genBlock:
				wants = append(wants, it.want())
			}
		}
		got, err := Parse(doc, Options{})
		if err != nil {
			t.Fatalf("tree %d: Parse(%q) = %v", i, doc, err)
		}
		if msg := problem(doc, got); msg != "" {
			t.Errorf("tree %d: Parse(%q): %s", i, doc, msg)
		}
		if gs := shapes(doc, got); !reflect.DeepEqual(gs, wants) {
			t.Fatalf("tree %d: Parse(%q) =\n%swant\n%s", i, doc, wantsText(gs), wantsText(wants))
		}
	}
}
