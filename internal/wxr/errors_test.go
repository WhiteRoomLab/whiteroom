package wxr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// errorCase is a file that Read rejects, with the Error that it returns. The place is that of the nth occurrence of at
// in in (the first by default), moved on by skip characters or past the whole of at if end is set, or the end of in if
// eof is set. encoding/xml finds most of its errors just after the problem, so those cases have end or skip.
type errorCase struct {
	name string
	in   string
	at   string
	nth  int
	skip int
	end  bool
	eof  bool
	msg  string
}

// where returns the place that the case expects.
func (tt errorCase) where(t testing.TB) Pos {
	t.Helper()
	s := strings.TrimPrefix(tt.in, "\xef\xbb\xbf")
	if tt.eof {
		return posAt(s, len(s))
	}
	nth := max(tt.nth, 1)
	idx := -1
	for i := 0; i < nth; i++ {
		j := strings.Index(s[idx+1:], tt.at)
		if j < 0 {
			t.Fatalf("%q does not occur %d times in %q", tt.at, nth, s)
		}
		idx += 1 + j
	}
	skip := tt.skip
	if tt.end {
		skip = len([]rune(tt.at))
	}
	for ; skip > 0; skip-- {
		_, n := utf8.DecodeRuneInString(s[idx:])
		idx += n
	}
	return posAt(s, idx)
}

const (
	outsideMsg = "unexpected text outside <rss>; WordPress may have written a PHP message into the export"
	invalidMsg = "invalid UTF-8; the WXR must be in UTF-8"
	decl       = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
)

// errorCases returns the files that Read rejects, which the fuzz test also starts from.
func errorCases(t testing.TB) []errorCase {
	noVersion := func(body string) string {
		return "<rss " + nsDecl + ">\n<channel>\n" + body + "</channel>\n</rss>\n"
	}
	versionDoc := func(v string) string {
		return strings.Replace(wxrDoc(""), "<wp:wxr_version>1.2</wp:wxr_version>", "<wp:wxr_version>"+v+"</wp:wxr_version>", 1)
	}
	post := "<wp:post_id>1</wp:post_id>\n<wp:post_type>post</wp:post_type>\n"
	typeOnly := "<wp:post_type>post</wp:post_type>\n"
	idDoc := func(id string) string { return itemDoc("<wp:post_id>" + id + "</wp:post_id>\n" + typeOnly) }

	tests := []errorCase{
		{name: "empty input", in: "", at: "", msg: "not a WXR file: missing <rss>"},
		{name: "only a comment", in: "<!-- x -->\n", eof: true, msg: "not a WXR file: missing <rss>"},
		{name: "text", in: "hello", at: "hello", msg: outsideMsg},
		{name: "a PHP message before the declaration", in: "Warning: undefined variable\n" + decl + wxrDoc(""), at: "Warning", msg: outsideMsg},
		{name: "text after white space", in: "\n\n  hello\n" + wxrDoc(""), at: "hello", msg: outsideMsg},
		{name: "the byte order mark is not counted", in: "\xef\xbb\xbfx", at: "x", msg: outsideMsg},
		{name: "UTF-16 little endian", in: "\xff\xfe<\x00r\x00", at: "", msg: "UTF-16 is not supported; convert the WXR to UTF-8"},
		{name: "UTF-16 big endian", in: "\xfe\xff\x00<\x00r", at: "", msg: "UTF-16 is not supported; convert the WXR to UTF-8"},
		{name: "invalid UTF-8 in text", in: itemDoc(post + "<title>a\xffb</title>\n"), at: "\xff", msg: invalidMsg},
		{name: "invalid UTF-8 in a CDATA section", in: itemDoc(post + "<title><![CDATA[a\xffb]]></title>\n"), at: "\xff", msg: invalidMsg},
		{name: "invalid UTF-8 in an attribute", in: itemDoc(post + "<category domain=\"a\xffb\" nicename=\"c\">x</category>\n"), at: "\xff", msg: invalidMsg},
		{name: "invalid UTF-8 in a comment", in: itemDoc(post + "<!-- \xff -->\n"), at: "\xff", msg: invalidMsg},
		{name: "invalid UTF-8 in a name", in: itemDoc(post + "<ti\xfftle>x</ti\xfftle>\n"), at: "\xff", msg: invalidMsg},
		{name: "invalid UTF-8 at the end", in: wxrDoc("") + "\xe3\x81", at: "\xe3\x81", msg: invalidMsg},
		{name: "an overlong character", in: wxrDoc("") + "\xc0\x80", at: "\xc0", msg: invalidMsg},
		{name: "a surrogate", in: wxrDoc("") + "\xed\xa0\x80", at: "\xed", msg: invalidMsg},
		{name: "a continuation byte", in: wxrDoc("") + "\x80", at: "\x80", msg: invalidMsg},
		{name: "invalid UTF-8 after Japanese text", in: itemDoc(post + "<title>あいう\xffえ</title>\n"), at: "\xff", msg: invalidMsg},
		{name: "EUC-JP", in: `<?xml version="1.0" encoding="EUC-JP"?>` + wxrDoc(""), at: "<?xml", msg: `unsupported encoding "EUC-JP"; convert the WXR to UTF-8`},
		{name: "UTF-16 in the declaration", in: "\n" + `<?xml version="1.0" encoding="UTF-16"?>` + wxrDoc(""), at: "<?xml", msg: `unsupported encoding "UTF-16"; convert the WXR to UTF-8`},
		{name: "XML 1.1", in: `<?xml version="1.1"?>` + wxrDoc(""), at: "<?xml", msg: `invalid XML: unsupported version "1.1"; only version 1.0 is supported`},
		{name: "DOCTYPE", in: "<!DOCTYPE rss>\n" + wxrDoc(""), at: "<!DOCTYPE", msg: "DOCTYPE is not allowed"},
		{name: "DOCTYPE with a subset", in: `<!DOCTYPE rss [<!ENTITY x "y">]>` + "\n" + wxrDoc(""), at: "<!DOCTYPE", msg: "DOCTYPE is not allowed"},
		{name: "a declaration in the channel", in: wxrDoc("<!FOO>\n"), at: "<!FOO>", msg: "DOCTYPE is not allowed"},
		{name: "a declaration in a value", in: itemDoc(post + "<title><!FOO></title>\n"), at: "<!FOO>", msg: "DOCTYPE is not allowed"},
		{name: "a declaration in an element that is skipped", in: wxrDoc("<x><!FOO></x>\n"), at: "<!FOO>", msg: "DOCTYPE is not allowed"},
		{name: "a declaration after the root", in: wxrDoc("") + "<!FOO>\n", at: "<!FOO>", msg: "DOCTYPE is not allowed"},
		{name: "an Atom feed", in: `<feed xmlns="http://www.w3.org/2005/Atom"></feed>`, at: "<feed", msg: "not a WXR file: the root element is <{http://www.w3.org/2005/Atom}feed>, not <rss>"},
		{name: "RSS in a namespace", in: `<rss xmlns="http://example.com/"><channel/></rss>`, at: "<rss", msg: "not a WXR file: the root element is <{http://example.com/}rss>, not <rss>"},
		{name: "HTML", in: "<html></html>", at: "<html>", msg: "not a WXR file: the root element is <html>, not <rss>"},
		{name: "no channel", in: "<rss>\n</rss>\n", at: "</rss>", msg: "not a WXR file: missing <channel> in <rss>"},
		{name: "two channels", in: head + "</channel>\n<channel>\n</channel>\n</rss>\n", at: "<channel>", nth: 2, msg: "more than one <channel> in <rss>"},
		{name: "text in rss", in: "<rss " + nsDecl + ">\nfoo\n" + "<channel/></rss>", at: "foo", msg: "unexpected text in <rss>"},
		{name: "no version before an item", in: noVersion("<item>\n</item>\n"), at: "<item>", msg: "not a WXR file: missing wp:wxr_version before <item>"},
		{name: "no version before an author", in: noVersion("<wp:author/>\n"), at: "<wp:author/>", msg: "not a WXR file: missing wp:wxr_version before <wp:author>"},
		{name: "no version before a category", in: noVersion("<wp:category/>\n"), at: "<wp:category/>", msg: "not a WXR file: missing wp:wxr_version before <wp:category>"},
		{name: "no version before a tag", in: noVersion("<wp:tag/>\n"), at: "<wp:tag/>", msg: "not a WXR file: missing wp:wxr_version before <wp:tag>"},
		{name: "no version before a term", in: noVersion("<wp:term/>\n"), at: "<wp:term/>", msg: "not a WXR file: missing wp:wxr_version before <wp:term>"},
		{name: "no version", in: noVersion("<title>t</title>\n"), at: "</channel>", msg: "not a WXR file: missing wp:wxr_version"},
		{name: "version 1.3", in: versionDoc("1.3"), at: "<wp:wxr_version>", msg: `unsupported WXR version "1.3"; use 1.0, 1.1 or 1.2`},
		{name: "version 1.2.1", in: versionDoc(" 1.2.1 "), at: "<wp:wxr_version>", msg: `unsupported WXR version "1.2.1"; use 1.0, 1.1 or 1.2`},
		{name: "an empty version", in: versionDoc(""), at: "<wp:wxr_version>", msg: `unsupported WXR version ""; use 1.0, 1.1 or 1.2`},
		{name: "two versions", in: wxrDoc("<wp:wxr_version>1.2</wp:wxr_version>\n"), at: "<wp:wxr_version>", nth: 2, msg: "duplicate <wp:wxr_version> in <channel>"},
		{name: "two titles in the channel", in: wxrDoc("<title>a</title>\n<title>b</title>\n"), at: "<title>", nth: 2, msg: "duplicate <title> in <channel>"},
		{name: "an undeclared prefix", in: "<rss><channel><wp:wxr_version>1.2</wp:wxr_version></channel></rss>", at: "<wp:wxr_version>", msg: `undeclared namespace prefix "wp"`},
		{name: "an undeclared prefix in an attribute", in: wxrDoc("<title a:b=\"1\">t</title>\n"), at: "<title a:b", msg: `undeclared namespace prefix "a"`},
		{name: "an undeclared prefix in an element that is skipped", in: wxrDoc("<foo:bar/>\n"), at: "<foo:bar/>", msg: `undeclared namespace prefix "foo"`},
		{name: "a named entity", in: itemDoc(post + "<title>&nbsp;</title>\n"), at: "&nbsp;", end: true, msg: "invalid XML: invalid character entity &nbsp;"},
		{name: "an ampersand", in: itemDoc(post + "<title>a & b</title>\n"), at: "& b", skip: 1, msg: "invalid XML: invalid character entity & (no semicolon)"},
		{name: "a character reference beyond Unicode", in: itemDoc(post + "<title>&#x110000;</title>\n"), at: "&#x110000;", end: true, msg: "invalid XML: invalid character entity &#x110000;"},
		{name: "a character reference to a control character", in: itemDoc(post + "<title>&#8;</title>\n"), at: "</title>", msg: "invalid XML: illegal character code U+0008"},
		{name: "a PHP message in the channel", in: wxrDoc("Warning: x\n"), at: "Warning", msg: "unexpected text in <channel>"},
		{name: "text after the root", in: wxrDoc("") + "trailing\n", at: "trailing", msg: outsideMsg},
		{name: "an element after the root", in: wxrDoc("") + "<extra/>\n", at: "<extra/>", msg: "unexpected element <extra> after </rss>"},
		{name: "a second root", in: wxrDoc("") + wxrDoc(""), at: "<rss", nth: 2, msg: "unexpected element <rss> after </rss>"},
		{name: "an element that is not closed", in: head + "<item>\n", eof: true, msg: "invalid XML: unexpected EOF"},
		{name: "a channel that is not closed", in: head, eof: true, msg: "invalid XML: unexpected EOF"},
		{name: "a mismatched end tag", in: head + "<item>\n</wp:post_id>\n" + tail, at: "</wp:post_id>", end: true, msg: "invalid XML: element <item> closed by </post_id>"},
		{name: "an end tag that opens nothing", in: "</x>", at: "</x>", end: true, msg: "invalid XML: unexpected end element </x>"},
		{name: "]]> in text", in: itemDoc(post + "<title>a]]>b</title>\n"), at: "]]>", end: true, msg: "invalid XML: unescaped ]]> not in CDATA section"},
		{name: "a CDATA section that is not closed", in: itemDoc(post + "<title><![CDATA[abc</title>\n"), eof: true, msg: "invalid XML: unexpected EOF in CDATA section"},
		{name: "< in an attribute", in: itemDoc(post + "<category domain=\"a<b\" nicename=\"c\"/>\n"), at: "<b\"", skip: 1, msg: "invalid XML: unescaped < inside quoted string"},
		{name: "an attribute without quotes", in: itemDoc(post + "<category domain=x nicename=\"c\"/>\n"), at: "domain=x", end: true, msg: "invalid XML: unquoted or missing attribute value in element"},
		{name: "an attribute without a value", in: itemDoc(post + "<category domain>x</category>\n"), at: "domain>", end: true, msg: "invalid XML: attribute name without = in element"},
		{name: "a tag that is not finished", in: itemDoc(post + "<title\n"), at: "</item>", msg: "invalid XML: expected attribute name in element"},
		{name: "an item without wp:post_id", in: itemDoc(typeOnly), at: "<item>", msg: "missing <wp:post_id> in <item>"},
		{name: "a wp:post_id that is not a number", in: idDoc("abc"), at: "<wp:post_id>", msg: `invalid <wp:post_id> "abc": use an integer from 1`},
		{name: "a wp:post_id of 0", in: idDoc("0"), at: "<wp:post_id>", msg: `invalid <wp:post_id> "0": use an integer from 1`},
		{name: "a negative wp:post_id", in: idDoc("-5"), at: "<wp:post_id>", msg: `invalid <wp:post_id> "-5": use an integer from 1`},
		{name: "an empty wp:post_id", in: idDoc(""), at: "<wp:post_id>", msg: `invalid <wp:post_id> "": use an integer from 1`},
		{name: "a wp:post_id with a fraction", in: idDoc(" 1.5 "), at: "<wp:post_id>", msg: `invalid <wp:post_id> "1.5": use an integer from 1`},
		{name: "a wp:post_id that is too large", in: idDoc("99999999999999999999"), at: "<wp:post_id>", msg: `invalid <wp:post_id> "99999999999999999999": use an integer from 1`},
		{name: "a negative wp:post_parent", in: itemDoc(post + "<wp:post_parent>-1</wp:post_parent>\n"), at: "<wp:post_parent>", msg: `invalid <wp:post_parent> "-1": use an integer from 0`},
		{name: "a wp:menu_order that is not a number", in: itemDoc(post + "<wp:menu_order>x</wp:menu_order>\n"), at: "<wp:menu_order>", msg: `invalid <wp:menu_order> "x": use an integer`},
		{name: "a wp:is_sticky that is not a number", in: itemDoc(post + "<wp:is_sticky>yes</wp:is_sticky>\n"), at: "<wp:is_sticky>", msg: `invalid <wp:is_sticky> "yes": use an integer`},
		{name: "an item without wp:post_type", in: itemDoc("<wp:post_id>1</wp:post_id>\n"), at: "<item>", msg: "missing <wp:post_type> in <item>"},
		{name: "an item with a blank wp:post_type", in: itemDoc("<wp:post_id>1</wp:post_id>\n<wp:post_type> \n</wp:post_type>\n"), at: "<item>", msg: "missing <wp:post_type> in <item>"},
		{name: "two wp:post_id in an item", in: itemDoc(post + "<wp:post_id>2</wp:post_id>\n"), at: "<wp:post_id>", nth: 2, msg: "duplicate <wp:post_id> in <item>"},
		{name: "two titles in an item", in: itemDoc(post + "<title>a</title><title>b</title>\n"), at: "<title>", nth: 2, msg: "duplicate <title> in <item>"},
		{name: "two content:encoded in an item", in: itemDoc(post + "<content:encoded>a</content:encoded><content:encoded>b</content:encoded>\n"), at: "<content:encoded>", nth: 2, msg: "duplicate <content:encoded> in <item>"},
		{name: "an element in a value", in: itemDoc(post + "<title><b>x</b></title>\n"), at: "<b>", msg: "unexpected element <b> in <title>"},
		{name: "a known element in a value", in: itemDoc(post + "<title><wp:post_id>1</wp:post_id></title>\n"), at: "<wp:post_id>", nth: 2, msg: "unexpected element <wp:post_id> in <title>"},
		{name: "an element of another namespace in a value", in: itemDoc(post + `<title><x:y xmlns:x="urn:x"/></title>` + "\n"), at: "<x:y", msg: "unexpected element <{urn:x}y> in <title>"},
		{name: "text in an item", in: itemDoc(post + "foo\n"), at: "foo", msg: "unexpected text in <item>"},
		{name: "text in an author", in: wxrDoc("<wp:author>\n  foo\n</wp:author>\n"), at: "foo", msg: "unexpected text in <wp:author>"},
		{name: "text in a postmeta", in: itemDoc(post + "<wp:postmeta>foo</wp:postmeta>\n"), at: "foo", msg: "unexpected text in <wp:postmeta>"},
		{name: "text after a comment in an item", in: itemDoc(post + "<!-- c -->foo\n"), at: "foo", msg: "unexpected text in <item>"},
		{name: "text in a CDATA section in an item", in: itemDoc(post + "<![CDATA[ foo]]>\n"), at: "foo", msg: "unexpected text in <item>"},
		{name: "a category without wp:category_nicename", in: wxrDoc("<wp:category><wp:term_id>1</wp:term_id></wp:category>\n"), at: "<wp:category>", msg: "missing <wp:category_nicename> in <wp:category>"},
		{name: "a category with a blank wp:category_nicename", in: wxrDoc("<wp:category><wp:category_nicename> </wp:category_nicename></wp:category>\n"), at: "<wp:category>", msg: "missing <wp:category_nicename> in <wp:category>"},
		{name: "a tag without wp:tag_slug", in: wxrDoc("<wp:tag><wp:tag_name>x</wp:tag_name></wp:tag>\n"), at: "<wp:tag>", msg: "missing <wp:tag_slug> in <wp:tag>"},
		{name: "a term without wp:term_taxonomy", in: wxrDoc("<wp:term><wp:term_slug>x</wp:term_slug></wp:term>\n"), at: "<wp:term>", msg: "missing <wp:term_taxonomy> in <wp:term>"},
		{name: "a term without wp:term_slug", in: wxrDoc("<wp:term><wp:term_taxonomy>t</wp:term_taxonomy></wp:term>\n"), at: "<wp:term>", msg: "missing <wp:term_slug> in <wp:term>"},
		{name: "a wp:term_id of 0", in: wxrDoc("<wp:tag><wp:term_id>0</wp:term_id><wp:tag_slug>x</wp:tag_slug></wp:tag>\n"), at: "<wp:term_id>", msg: `invalid <wp:term_id> "0": use an integer from 1`},
		{name: "an author without wp:author_login", in: wxrDoc("<wp:author><wp:author_id>1</wp:author_id></wp:author>\n"), at: "<wp:author>", msg: "missing <wp:author_login> in <wp:author>"},
		{name: "a wp:author_id that is not a number", in: wxrDoc("<wp:author><wp:author_id>x</wp:author_id><wp:author_login>a</wp:author_login></wp:author>\n"), at: "<wp:author_id>", msg: `invalid <wp:author_id> "x": use an integer from 1`},
		{name: "a postmeta without wp:meta_key", in: itemDoc(post + "<wp:postmeta><wp:meta_value>x</wp:meta_value></wp:postmeta>\n"), at: "<wp:postmeta>", msg: "missing <wp:meta_key> in <wp:postmeta>"},
		{name: "a termmeta without wp:meta_key", in: wxrDoc("<wp:category><wp:category_nicename>c</wp:category_nicename><wp:termmeta/></wp:category>\n"), at: "<wp:termmeta/>", msg: "missing <wp:meta_key> in <wp:termmeta>"},
		{name: "two wp:meta_value in a postmeta", in: itemDoc(post + "<wp:postmeta><wp:meta_key>k</wp:meta_key><wp:meta_value>1</wp:meta_value><wp:meta_value>2</wp:meta_value></wp:postmeta>\n"), at: "<wp:meta_value>", nth: 2, msg: "duplicate <wp:meta_value> in <wp:postmeta>"},
	}

	// The cases that name the first record in the message.
	items := wxrDoc("<item>\n" + post + "</item>\n<item>\n" + post + "</item>\n")
	firstItem := place(t, items, "<item>", 1)
	cats := wxrDoc("<wp:category><wp:term_id>1</wp:term_id><wp:category_nicename>a</wp:category_nicename></wp:category>\n" +
		"<wp:category><wp:term_id>1</wp:term_id><wp:category_nicename>b</wp:category_nicename></wp:category>\n")
	firstCat := place(t, cats, "<wp:category>", 1)
	idThenTerm := wxrDoc("<wp:category><wp:term_id>2</wp:term_id><wp:category_nicename>a</wp:category_nicename></wp:category>\n" +
		"<wp:term><wp:term_id>2</wp:term_id><wp:term_taxonomy>category</wp:term_taxonomy><wp:term_slug>b</wp:term_slug></wp:term>\n")
	slugs := wxrDoc("<wp:tag><wp:tag_slug>x</wp:tag_slug></wp:tag>\n<wp:tag><wp:tag_slug>x</wp:tag_slug></wp:tag>\n")
	firstTag := place(t, slugs, "<wp:tag>", 1)
	catSlug := wxrDoc("<wp:category><wp:category_nicename>x</wp:category_nicename></wp:category>\n" +
		"<wp:term><wp:term_taxonomy>category</wp:term_taxonomy><wp:term_slug>x</wp:term_slug></wp:term>\n")
	firstCatSlug := place(t, catSlug, "<wp:category>", 1)
	logins := wxrDoc("<wp:author><wp:author_login>a</wp:author_login></wp:author>\n<wp:author><wp:author_login>b</wp:author_login></wp:author>\n" +
		"<wp:author><wp:author_login>a</wp:author_login></wp:author>\n")
	firstLogin := place(t, logins, "<wp:author>", 1)
	tests = append(tests,
		errorCase{name: "the same wp:post_id", in: items, at: "<item>", nth: 2,
			msg: fmt.Sprintf("duplicate wp:post_id 1; the first is at %d:%d", firstItem.Line, firstItem.Column)},
		errorCase{name: "the same wp:term_id in a taxonomy", in: cats, at: "<wp:category>", nth: 2,
			msg: fmt.Sprintf(`duplicate wp:term_id 1 in the taxonomy "category"; the first is at %d:%d`, firstCat.Line, firstCat.Column)},
		errorCase{name: "the same wp:term_id in a taxonomy, in a wp:term", in: idThenTerm, at: "<wp:term>",
			msg: fmt.Sprintf(`duplicate wp:term_id 2 in the taxonomy "category"; the first is at %d:%d`,
				place(t, idThenTerm, "<wp:category>", 1).Line, place(t, idThenTerm, "<wp:category>", 1).Column)},
		errorCase{name: "the same slug in a taxonomy", in: slugs, at: "<wp:tag>", nth: 2,
			msg: fmt.Sprintf(`duplicate slug "x" in the taxonomy "post_tag"; the first is at %d:%d`, firstTag.Line, firstTag.Column)},
		errorCase{name: "the same slug in a taxonomy, in a wp:term", in: catSlug, at: "<wp:term>",
			msg: fmt.Sprintf(`duplicate slug "x" in the taxonomy "category"; the first is at %d:%d`, firstCatSlug.Line, firstCatSlug.Column)},
		errorCase{name: "the same login", in: logins, at: "<wp:author>", nth: 3,
			msg: fmt.Sprintf(`duplicate wp:author_login "a"; the first is at %d:%d`, firstLogin.Line, firstLogin.Column)},
	)

	return tests
}

func TestReadErrors(t *testing.T) {
	for _, tt := range errorCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			ex, err := Read(context.Background(), strings.NewReader(tt.in), Options{})
			if ex != nil {
				t.Errorf("Read() returned an Export with the error %v", err)
			}
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Read() = %v, %T, want an *Error", err, err)
			}
			pos := tt.where(t)
			want := fmt.Sprintf("%d:%d: %s", pos.Line, pos.Column, tt.msg)
			if got := err.Error(); got != want {
				t.Errorf("Read() = %q, want %q", got, want)
			}
			if e.Pos != pos || e.Msg != tt.msg {
				t.Errorf("Read() = {%v, %q}, want {%v, %q}", e.Pos, e.Msg, pos, tt.msg)
			}
		})
	}
}

// TestSameTermIDInOtherTaxonomies checks the other side of the duplicates: the same ID or slug in another taxonomy is
// not one.
func TestSameTermIDInOtherTaxonomies(t *testing.T) {
	ex := read(t, wxrDoc(`<wp:category><wp:term_id>1</wp:term_id><wp:category_nicename>x</wp:category_nicename></wp:category>
<wp:tag><wp:term_id>1</wp:term_id><wp:tag_slug>x</wp:tag_slug></wp:tag>
`))
	if len(ex.Terms) != 2 {
		t.Errorf("%d terms, want 2", len(ex.Terms))
	}
}
