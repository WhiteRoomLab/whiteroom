package wxr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// readWith reads in with opts, and returns the error as an *Error, or nil if there is none.
func readWith(t *testing.T, in string, opts Options) *Error {
	t.Helper()
	ex, err := Read(context.Background(), strings.NewReader(in), opts)
	if err == nil {
		if ex == nil {
			t.Fatal("Read() = nil, nil")
		}
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("Read() = %v, %T, want an *Error", err, err)
	}
	if ex != nil {
		t.Errorf("Read() returned an Export with the error %v", err)
	}
	return e
}

// atLimit checks that Read accepts in with the options that have the limit, and rejects it with the message at the
// place of the nth occurrence of at with the options that have one less.
func atLimit(t *testing.T, in string, atOpts, overOpts Options, at string, nth int, msg string) {
	t.Helper()
	if e := readWith(t, in, atOpts); e != nil {
		t.Errorf("Read() with %+v = %v, want it to read the file", atOpts, e)
	}
	e := readWith(t, in, overOpts)
	if e == nil {
		t.Fatalf("Read() with %+v read the file, want the error %q", overOpts, msg)
	}
	if want := place(t, in, at, nth); e.Pos != want || e.Msg != msg {
		t.Errorf("Read() with %+v = %v, want %d:%d: %s", overOpts, e, want.Line, want.Column, msg)
	}
}

func TestLimitMaxBytes(t *testing.T) {
	bs := string(rune(0x08))
	docs := []struct{ name, in string }{
		{"plain", wxrDoc("")},
		{"byte order mark", "\xef\xbb\xbf" + wxrDoc("")},
		{"removed characters", wxrDoc("<title>a" + bs + bs + "b</title>\n")},
		{"multi-byte characters", wxrDoc("<title>あいう</title>\n")},
		{"CRLF", strings.ReplaceAll(wxrDoc(""), "\n", "\r\n")},
	}
	for _, d := range docs {
		t.Run(d.name, func(t *testing.T) {
			n := int64(len(d.in))
			// The last byte is the one over the limit, and it is the last character: the line break.
			s := strings.TrimPrefix(d.in, "\xef\xbb\xbf")
			last := strings.LastIndex(s, "\n")
			wantPos := posAt(s, last)
			over := Options{MaxBytes: n - 1}
			e := readWith(t, d.in, Options{MaxBytes: n})
			if e != nil {
				t.Fatalf("Read() with the size of the file as the limit = %v", e)
			}
			e = readWith(t, d.in, over)
			if e == nil {
				t.Fatal("Read() with one byte less read the file")
			}
			if msg := fmt.Sprintf("too large: at least %d bytes (max %d)", n, n-1); e.Pos != wantPos || e.Msg != msg {
				t.Errorf("Read() = %v, want %d:%d: %s", e, wantPos.Line, wantPos.Column, msg)
			}
		})
	}

	t.Run("in a character", func(t *testing.T) {
		in := wxrDoc("<title>あいう</title>\n")
		// The limit is after the first byte of あ, so that character is the one over it.
		idx := strings.Index(in, "あ")
		msg := fmt.Sprintf("too large: at least %d bytes (max %d)", idx+2, idx+1)
		e := readWith(t, in, Options{MaxBytes: int64(idx + 1)})
		if want := place(t, in, "あ", 1); e == nil || e.Pos != want || e.Msg != msg {
			t.Errorf("Read() = %v, want %d:%d: %s", e, want.Line, want.Column, msg)
		}
	})
	t.Run("in the file that is invalid before the limit", func(t *testing.T) {
		in := wxrDoc("<title>a\xffb</title>\n")
		if e := readWith(t, in, Options{MaxBytes: int64(len(in))}); e == nil || e.Msg != invalidMsg {
			t.Errorf("Read() = %v, want %q", e, invalidMsg)
		}
	})
}

func TestLimitMaxItems(t *testing.T) {
	atLimit(t, itemsWithIDs(3), Options{MaxItems: 3}, Options{MaxItems: 2}, "<item>", 3, "too many items: at least 3 (max 2)")
}

// itemsWithIDs returns a WXR with n items that have the IDs 1 to n.
func itemsWithIDs(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "<item>\n<wp:post_id>%d</wp:post_id>\n<wp:post_type>post</wp:post_type>\n</item>\n", i)
	}
	return wxrDoc(sb.String())
}

func TestLimitMaxTerms(t *testing.T) {
	in := wxrDoc(`<wp:category><wp:category_nicename>a</wp:category_nicename></wp:category>
<wp:tag><wp:tag_slug>b</wp:tag_slug></wp:tag>
<wp:term><wp:term_taxonomy>t</wp:term_taxonomy><wp:term_slug>c</wp:term_slug></wp:term>
`)
	atLimit(t, in, Options{MaxTerms: 3}, Options{MaxTerms: 2}, "<wp:term>", 1, "too many terms: at least 3 (max 2)")
	atLimit(t, in, Options{MaxTerms: 4}, Options{MaxTerms: 1}, "<wp:tag>", 1, "too many terms: at least 2 (max 1)")
}

func TestLimitMaxAuthors(t *testing.T) {
	in := wxrDoc(`<wp:author><wp:author_login>a</wp:author_login></wp:author>
<wp:author><wp:author_login>b</wp:author_login></wp:author>
`)
	atLimit(t, in, Options{MaxAuthors: 2}, Options{MaxAuthors: 1}, "<wp:author>", 2, "too many authors: at least 2 (max 1)")
}

func TestLimitMaxDepth(t *testing.T) {
	// <rss> is 1 and <channel> is 2.
	deep := func(n int) string {
		return wxrDoc(strings.Repeat("<a>", n) + strings.Repeat("</a>", n) + "\n")
	}
	atLimit(t, deep(3), Options{MaxDepth: 5}, Options{MaxDepth: 4}, "<a>", 3, "elements are nested too deeply: at least 5 levels (max 4)")
	atLimit(t, deep(4), Options{MaxDepth: 6}, Options{MaxDepth: 5}, "<a>", 4, "elements are nested too deeply: at least 6 levels (max 5)")
	// The elements that are read count as well: <item> is 3, <wp:post_id> and <wp:postmeta> are 4, and <wp:meta_key> is 5.
	meta := itemDoc(minimalItem + "<wp:postmeta><wp:meta_key>k</wp:meta_key></wp:postmeta>\n")
	atLimit(t, meta, Options{MaxDepth: 5}, Options{MaxDepth: 4}, "<wp:meta_key>", 1, "elements are nested too deeply: at least 5 levels (max 4)")
	atLimit(t, itemDoc(minimalItem), Options{MaxDepth: 4}, Options{MaxDepth: 3}, "<wp:post_id>", 1, "elements are nested too deeply: at least 4 levels (max 3)")
	// An element in a value is too deep before it is found to be in the wrong place.
	value := itemDoc(minimalItem + "<title><b>x</b></title>\n")
	e := readWith(t, value, Options{MaxDepth: 4})
	if want := place(t, value, "<b>", 1); e == nil || e.Pos != want || e.Msg != "elements are nested too deeply: at least 5 levels (max 4)" {
		t.Errorf("Read() = %v, want the error at %v", e, want)
	}
}

func TestLimitMaxTagBytes(t *testing.T) {
	rssTag := len(`<rss version="2.0" ` + nsDecl + ">")
	long := strings.Repeat("v", 1000)

	t.Run("the root tag", func(t *testing.T) {
		in := wxrDoc("")
		atLimit(t, in, Options{MaxTagBytes: rssTag}, Options{MaxTagBytes: rssTag - 1}, "<rss", 1,
			fmt.Sprintf("the tag is too long: at least %d bytes (max %d)", rssTag, rssTag-1))
	})
	// tag checks the limit with the longest tag of s, which begins with at and has n bytes.
	tag := func(name, s, at string, n int) {
		t.Run(name, func(t *testing.T) {
			in := wxrDoc(s + "\n")
			atLimit(t, in, Options{MaxTagBytes: n}, Options{MaxTagBytes: n - 1}, at, 1,
				fmt.Sprintf("the tag is too long: at least %d bytes (max %d)", n, n-1))
		})
	}
	single := func(name, s string) { tag(name, s, s[:3], len(s)) }
	single("a start tag", `<x a="`+long+`"/>`)
	endTag := `</x` + strings.Repeat(" ", 1000) + `>`
	tag("an end tag", `<x>`+endTag, "</x", len(endTag))
	single("> in double quotes", `<x a="`+strings.Repeat(">", 1000)+`"/>`)
	single("> in single quotes", `<x a='`+strings.Repeat(">", 1000)+`'/>`)
	single("quotes in quotes", `<x a="'`+strings.Repeat(">", 500)+`'" b='"`+strings.Repeat(">", 500)+`"'/>`)
	single("two attributes", `<x a="`+long+`" b='`+long+`'/>`)

	t.Run("the other things in the file are not counted", func(t *testing.T) {
		big := strings.Repeat("x", 5000)
		in := wxrDoc("<!-- " + big + " -->\n<?pi " + big + "?>\n<item>\n" + minimalItem +
			"<content:encoded><![CDATA[" + big + "]]></content:encoded>\n<title>" + big + "</title>\n</item>\n")
		if e := readWith(t, in, Options{MaxTagBytes: rssTag}); e != nil {
			t.Errorf("Read() = %v, want the comment, the CDATA section, the processing instruction and the text not to count", e)
		}
	})
	t.Run("a comment that has a > and quotes", func(t *testing.T) {
		in := wxrDoc("<!-- a > b \" c ' d -->\n<?pi > \" ?>\n<item>\n" + minimalItem + "<title><![CDATA[>\"']]></title></item>\n")
		if e := readWith(t, in, Options{MaxTagBytes: rssTag}); e != nil {
			t.Errorf("Read() = %v", e)
		}
	})
	t.Run("a declaration", func(t *testing.T) {
		in := "<!DOCTYPE " + long + ">\n" + wxrDoc("")
		n := len("<!DOCTYPE " + long + ">")
		if e := readWith(t, in, Options{MaxTagBytes: n}); e == nil || e.Msg != "DOCTYPE is not allowed" {
			t.Errorf("Read() = %v, want the DOCTYPE error", e)
		}
		msg := fmt.Sprintf("the tag is too long: at least %d bytes (max %d)", n, n-1)
		if e := readWith(t, in, Options{MaxTagBytes: n - 1}); e == nil || e.Msg != msg || e.Pos != (Pos{1, 1}) {
			t.Errorf("Read() = %v, want 1:1: %s", e, msg)
		}
	})
	t.Run("a tag that is not closed", func(t *testing.T) {
		in := wxrDoc("<x a=\"" + strings.Repeat("v", 5000))
		e := readWith(t, in, Options{MaxTagBytes: 1000})
		if e == nil || !strings.HasPrefix(e.Msg, "the tag is too long") {
			t.Errorf("Read() = %v, want the tag to be too long", e)
		}
	})
	t.Run("many attributes", func(t *testing.T) {
		// encoding/xml keeps all the attributes of a tag, which is what the limit is for.
		in := wxrDoc("<x" + strings.Repeat(` a="1"`, 15000) + "/>\n")
		e := readWith(t, in, Options{})
		if e == nil || !strings.HasPrefix(e.Msg, "the tag is too long") {
			t.Errorf("Read() = %v, want the tag to be too long", e)
		}
	})
}

func TestOptionsDefaults(t *testing.T) {
	want := Options{MaxBytes: 1 << 30, MaxItems: 1_000_000, MaxTerms: 1_000_000, MaxAuthors: 100_000, MaxDepth: 64, MaxTagBytes: 65536}
	if got := (Options{}).withDefaults(); got != want {
		t.Errorf("Options{}.withDefaults() = %+v, want %+v", got, want)
	}
	if got := (Options{MaxBytes: -1, MaxItems: -1, MaxTerms: -5, MaxAuthors: -1, MaxDepth: -1, MaxTagBytes: -1}).withDefaults(); got != want {
		t.Errorf("a negative limit: withDefaults() = %+v, want %+v", got, want)
	}
	got := Options{MaxBytes: 5, MaxItems: 6}.withDefaults()
	if want := (Options{MaxBytes: 5, MaxItems: 6, MaxTerms: 1_000_000, MaxAuthors: 100_000, MaxDepth: 64, MaxTagBytes: 65536}); got != want {
		t.Errorf("withDefaults() = %+v, want %+v", got, want)
	}
	// A file that is read with the default limits is not limited by the small ones that the tests use.
	if e := readWith(t, itemsWithIDs(50), Options{}); e != nil {
		t.Errorf("Read() = %v", e)
	}
}
