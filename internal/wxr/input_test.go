package wxr

import (
	"encoding/xml"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// isEOF reports whether err is io.EOF itself, which encoding/xml compares its errors with.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) && errors.Unwrap(err) == nil
}

func mustReadByte(t *testing.T, in *input) {
	t.Helper()
	if _, err := in.ReadByte(); err != nil {
		t.Fatalf("ReadByte() = %v", err)
	}
}

func testInput(s string, maxBytes int64, maxTag int) *input {
	if maxBytes == 0 {
		maxBytes = MaxBytes
	}
	if maxTag == 0 {
		maxTag = MaxTagBytes
	}
	return newInput(strings.NewReader(s), Options{MaxBytes: maxBytes, MaxTagBytes: maxTag})
}

// readAll reads in to the end, or to the first error, and returns the bytes with the place of each.
func readAll(in *input) (data []byte, places []Pos, err error) {
	for {
		b, err := in.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return data, places, err
		}
		data = append(data, b)
		places = append(places, in.lastPos)
	}
}

func TestInputPositions(t *testing.T) {
	// A carriage return is a character and a line feed ends the line. A character is the same place for all its bytes.
	in := testInput("a\nbc\r\nあ\tz", 0, 0)
	data, places, err := readAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a\nbc\r\nあ\tz" {
		t.Errorf("data = %q", data)
	}
	want := []Pos{{1, 1}, {1, 2}, {2, 1}, {2, 2}, {2, 3}, {2, 4}, {3, 1}, {3, 1}, {3, 1}, {3, 2}, {3, 3}}
	if !reflect.DeepEqual(places, want) {
		t.Errorf("places = %v, want %v", places, want)
	}
}

func TestInputByteOrderMark(t *testing.T) {
	data, places, err := readAll(testInput("\xef\xbb\xbfab", 0, 0))
	if err != nil || string(data) != "ab" || !reflect.DeepEqual(places, []Pos{{1, 1}, {1, 2}}) {
		t.Errorf("a byte order mark: %q, %v, %v, want ab at 1:1 and 1:2", data, places, err)
	}
	// Only the first one is a byte order mark, and the others are characters.
	data, places, err = readAll(testInput("a\xef\xbb\xbfb", 0, 0))
	if err != nil || string(data) != "a\xef\xbb\xbfb" || !reflect.DeepEqual(places, []Pos{{1, 1}, {1, 2}, {1, 2}, {1, 2}, {1, 3}}) {
		t.Errorf("a character U+FEFF: %q, %v, %v", data, places, err)
	}
	// Two byte order marks are a byte order mark and a character.
	data, _, err = readAll(testInput("\xef\xbb\xbf\xef\xbb\xbf", 0, 0))
	if err != nil || string(data) != "\xef\xbb\xbf" {
		t.Errorf("two byte order marks: %q, %v", data, err)
	}
	for _, s := range []string{"\xff\xfe", "\xfe\xff", "\xff\xfe\x00\x00", "\xfe\xff\x00a"} {
		_, _, err := readAll(testInput(s, 0, 0))
		var e *Error
		if !errors.As(err, &e) || e.Pos != (Pos{1, 1}) || e.Msg != "UTF-16 is not supported; convert the WXR to UTF-8" {
			t.Errorf("%q: err = %v, want the error for UTF-16 at 1:1", s, err)
		}
	}
	// One of the bytes alone is invalid UTF-8.
	for _, s := range []string{"\xff", "\xfe", "\xff "} {
		_, _, err := readAll(testInput(s, 0, 0))
		var e *Error
		if !errors.As(err, &e) || e.Pos != (Pos{1, 1}) || e.Msg != invalidMsg {
			t.Errorf("%q: err = %v, want invalid UTF-8 at 1:1", s, err)
		}
	}
}

func TestInputRemoved(t *testing.T) {
	u := func(r rune) string { return string(r) }
	s := "a" + u(8) + "b" + u(0x1f) + u(0x0b) + "\nc" + u(0xfffe) + u(0xffff) + u(0x0c) + u(0x0e)
	in := testInput(s, 0, 0)
	data, _, err := readAll(in)
	if err != nil || string(data) != "ab\nc" {
		t.Fatalf("data = %q, %v, want ab, a line feed and c", data, err)
	}
	list, n := in.drain()
	want := []removedChar{
		{Pos{1, 2}, 0x08}, {Pos{1, 4}, 0x1f}, {Pos{1, 5}, 0x0b}, {Pos{2, 2}, 0xfffe}, {Pos{2, 3}, 0xffff}, {Pos{2, 4}, 0x0c}, {Pos{2, 5}, 0x0e},
	}
	if n != len(want) || !reflect.DeepEqual(list, want) {
		t.Errorf("drain() = %v, %d, want %v, %d", list, n, want, len(want))
	}
	if list, n := in.drain(); n != 0 || len(list) != 0 {
		t.Errorf("a second drain() = %v, %d, want none", list, n)
	}

	// What XML allows stays: tab, line feed, carriage return, DEL, the C1 controls, U+FFFD and the noncharacters of the
	// supplementary planes.
	allowed := "\t\n\r" + u(0x7f) + u(0x80) + u(0x85) + u(0x9f) + u(0xfffd) + u(0x1fffe) + u(0x10ffff) + u(0xe000) + u(0xd7ff)
	in = testInput(allowed, 0, 0)
	data, _, err = readAll(in)
	if err != nil || string(data) != allowed {
		t.Errorf("data = %q, %v, want it unchanged", data, err)
	}
	if _, n := in.drain(); n != 0 {
		t.Errorf("drain() = %d characters, want none", n)
	}
}

func TestInputRemovedMany(t *testing.T) {
	in := testInput("a"+strings.Repeat(string(rune(8)), 15)+"b", 0, 0)
	if _, _, err := readAll(in); err != nil {
		t.Fatal(err)
	}
	list, n := in.drain()
	if n != 15 || len(list) != maxListed {
		t.Fatalf("drain() = %d listed, %d, want %d listed, 15", len(list), n, maxListed)
	}
	for i, c := range list {
		if want := (Pos{1, 2 + i}); c.pos != want || c.r != 8 {
			t.Errorf("list[%d] = %v, want U+0008 at %v", i, c, want)
		}
	}
}

// TestInputPos asks for the places the way the decoder does: with the offset after the bytes that were passed on, or
// one before it if the decoder has put a byte back.
func TestInputPos(t *testing.T) {
	t.Run("ASCII", func(t *testing.T) {
		in := testInput("ab", 0, 0)
		if got := in.pos(0); got != (Pos{1, 1}) {
			t.Errorf("pos(0) at the start = %v, want 1:1", got)
		}
		mustReadByte(t, in)
		if got := in.pos(1); got != (Pos{1, 2}) {
			t.Errorf("pos(1) after a = %v, want 1:2", got)
		}
		if got := in.pos(0); got != (Pos{1, 1}) {
			t.Errorf("pos(0) after a, as if it was put back = %v, want 1:1", got)
		}
		if b, err := in.ReadByte(); err != nil || b != 'b' {
			t.Fatalf("ReadByte() = %q, %v, want b: looking ahead has not changed what is read", b, err)
		}
		if got := in.pos(2); got != (Pos{1, 3}) {
			t.Errorf("pos(2) at the end = %v, want 1:3", got)
		}
		if _, err := in.ReadByte(); !isEOF(err) {
			t.Errorf("ReadByte() = %v, want io.EOF", err)
		}
		if got := in.pos(2); got != (Pos{1, 3}) {
			t.Errorf("pos(2) after the end = %v, want 1:3", got)
		}
	})
	t.Run("removed characters", func(t *testing.T) {
		in := testInput("a\x08\x0bb\x08", 0, 0)
		mustReadByte(t, in)
		// b is the next byte, after two characters that are removed.
		if got := in.pos(1); got != (Pos{1, 4}) {
			t.Errorf("pos(1) = %v, want 1:4", got)
		}
		if list, n := in.drain(); n != 2 || list[0].pos != (Pos{1, 2}) || list[1].pos != (Pos{1, 3}) {
			t.Errorf("drain() = %v, %d, want the two characters before b", list, n)
		}
		mustReadByte(t, in)
		// The end is after the character that is removed last.
		if got := in.pos(2); got != (Pos{1, 6}) {
			t.Errorf("pos(2) = %v, want 1:6", got)
		}
		if _, n := in.drain(); n != 1 {
			t.Errorf("drain() = %d, want the last character", n)
		}
	})
	t.Run("a character of three bytes", func(t *testing.T) {
		in := testInput("あい", 0, 0)
		mustReadByte(t, in)
		if got := in.pos(1); got != (Pos{1, 1}) {
			t.Errorf("pos(1) in あ = %v, want 1:1", got)
		}
		mustReadByte(t, in)
		mustReadByte(t, in)
		if got := in.pos(3); got != (Pos{1, 2}) {
			t.Errorf("pos(3) before い = %v, want 1:2", got)
		}
		if got := in.pos(2); got != (Pos{1, 1}) {
			t.Errorf("pos(2) with the last byte of あ put back = %v, want 1:1", got)
		}
	})
	t.Run("a byte order mark", func(t *testing.T) {
		in := testInput("\xef\xbb\xbf\nx", 0, 0)
		if got := in.pos(0); got != (Pos{1, 1}) {
			t.Errorf("pos(0) = %v, want 1:1", got)
		}
		mustReadByte(t, in)
		if got := in.pos(1); got != (Pos{2, 1}) {
			t.Errorf("pos(1) = %v, want 2:1", got)
		}
	})
	t.Run("an error", func(t *testing.T) {
		in := testInput("a\xffb", 0, 0)
		mustReadByte(t, in)
		// The place of a byte that is not valid is the place of the error, and the error stays.
		if got := in.pos(1); got != (Pos{1, 2}) {
			t.Errorf("pos(1) = %v, want 1:2", got)
		}
		_, err := in.ReadByte()
		var e *Error
		if !errors.As(err, &e) || e.Pos != (Pos{1, 2}) || e.Msg != invalidMsg {
			t.Errorf("ReadByte() = %v, want invalid UTF-8 at 1:2", err)
		}
	})
}

func TestInputSize(t *testing.T) {
	// The byte order mark and the characters that are removed are bytes of the file.
	const s = "\xef\xbb\xbfa\x08b"
	if _, _, err := readAll(testInput(s, int64(len(s)), 0)); err != nil {
		t.Errorf("a limit of the size of the file: %v", err)
	}
	_, _, err := readAll(testInput(s, int64(len(s))-1, 0))
	var e *Error
	if !errors.As(err, &e) || e.Pos != (Pos{1, 3}) || e.Msg != "too large: at least 6 bytes (max 5)" {
		t.Errorf("a limit of one less: %v, want 1:3: too large: at least 6 bytes (max 5)", err)
	}
	// The error stays, the same one.
	in := testInput("abc", 2, 0)
	data, _, err1 := readAll(in)
	if string(data) != "ab" || err1 == nil {
		t.Fatalf("data = %q, %v, want ab and an error", data, err1)
	}
	if _, err2 := in.ReadByte(); !errors.Is(err2, err1) {
		t.Errorf("ReadByte() = %v, want %v again", err2, err1)
	}
	// A character that is over the limit with some of its bytes is the one that the error is at.
	_, _, err = readAll(testInput("aあ", 2, 0))
	if !errors.As(err, &e) || e.Pos != (Pos{1, 2}) || e.Msg != "too large: at least 3 bytes (max 2)" {
		t.Errorf("a limit in a character: %v", err)
	}
}

func TestInputTags(t *testing.T) {
	long := strings.Repeat("x", 100)
	tests := []struct {
		name   string
		in     string
		maxTag int
		at     Pos // of the error, or the zero value if there is none
		msg    string
	}{
		{name: "a start tag", in: "<ab c=\"d\">", maxTag: 10},
		{name: "a start tag, one over", in: "<ab c=\"d\">", maxTag: 9, at: Pos{1, 1}, msg: "the tag is too long: at least 10 bytes (max 9)"},
		{name: "an end tag", in: "<a></abc>", maxTag: 6},
		{name: "an end tag, one over", in: "<a></abc>", maxTag: 5, at: Pos{1, 4}, msg: "the tag is too long: at least 6 bytes (max 5)"},
		{name: "an empty element", in: "<a/>", maxTag: 4},
		{name: "an empty element, one over", in: "\n  <a/>", maxTag: 3, at: Pos{2, 3}, msg: "the tag is too long: at least 4 bytes (max 3)"},
		{name: "> in double quotes", in: `<a b=">">`, maxTag: 9},
		{name: "> in double quotes, one over", in: `<a b=">">`, maxTag: 8, at: Pos{1, 1}, msg: "the tag is too long: at least 9 bytes (max 8)"},
		{name: "> in single quotes", in: `<a b='>'>`, maxTag: 9},
		{name: "> in single quotes, one over", in: `<a b='>'>`, maxTag: 8, at: Pos{1, 1}, msg: "the tag is too long: at least 9 bytes (max 8)"},
		{name: "a quote in the other quotes", in: `<a b="'>" c='">'>`, maxTag: 17},
		{name: "a quote in the other quotes, one over", in: `<a b="'>" c='">'>`, maxTag: 16, at: Pos{1, 1}, msg: "the tag is too long: at least 17 bytes (max 16)"},
		{name: "text is not counted", in: "<a>" + long + "<b>", maxTag: 3},
		{name: "a declaration", in: "<!DOCTYPE a>", maxTag: 12},
		{name: "a declaration, one over", in: "<!DOCTYPE a>", maxTag: 11, at: Pos{1, 1}, msg: "the tag is too long: at least 12 bytes (max 11)"},
		{name: "> in the quotes of a declaration", in: `<!DOCTYPE a "x>y">`, maxTag: 18},
		{name: "> in the quotes of a declaration, one over", in: `<!DOCTYPE a "x>y">`, maxTag: 17, at: Pos{1, 1}, msg: "the tag is too long: at least 18 bytes (max 17)"},
		{name: "a declaration whose first byte is >", in: "<!>x>", maxTag: 5},
		{name: "a declaration whose first byte is >, one over", in: "<!>x>", maxTag: 4, at: Pos{1, 1}, msg: "the tag is too long: at least 5 bytes (max 4)"},
		{name: "a comment is not counted", in: "<!--" + long + "-->", maxTag: 3},
		{name: "a comment with a > and tags", in: "<!-- > <a b> -- -> --><a>", maxTag: 3},
		{name: "an empty comment", in: "<!----><a>", maxTag: 3},
		{name: "a comment that <!---> does not end", in: "<!---><" + long + ">-->" + "<a>", maxTag: 3},
		{name: "a comment that is not closed", in: "<!-- " + long, maxTag: 3},
		{name: "a CDATA section is not counted", in: "<![CDATA[" + long + "]]><a>", maxTag: 3},
		{name: "a CDATA section with brackets", in: "<![CDATA[a]b]]c]]]]><![CDATA[>" + long + "]]><a>", maxTag: 3},
		{name: "a CDATA section with tags", in: "<![CDATA[<a b=\"" + long + "\">]]><a>", maxTag: 3},
		{name: "a CDATA section that is not closed", in: "<![CDATA[" + long, maxTag: 3},
		{name: "a processing instruction is not counted", in: "<?xml version=\"1.0\" " + long + "?><a>", maxTag: 3},
		{name: "a processing instruction with ? and >", in: "<?a ? > ?? ?>" + long + "<a>", maxTag: 3},
		{name: "text after a processing instruction", in: "<?a?>" + long + "<a>", maxTag: 3},
		{name: "a tag after a declaration", in: "<!DOCTYPE a><abcdef>", maxTag: 12},
		{name: "a tag after a declaration, one over", in: "<!DOCTYPE a><abcdef>", maxTag: 7, at: Pos{1, 1}, msg: "the tag is too long: at least 8 bytes (max 7)"},
		{name: "a tag after a comment, one over", in: "<!-- a --><abcdef>", maxTag: 6, at: Pos{1, 11}, msg: "the tag is too long: at least 7 bytes (max 6)"},
		{name: "a tag after a CDATA section, one over", in: "<![CDATA[a]]><abcdef>", maxTag: 6, at: Pos{1, 14}, msg: "the tag is too long: at least 7 bytes (max 6)"},
		{name: "a tag after a processing instruction, one over", in: "<?a?><abcdef>", maxTag: 6, at: Pos{1, 6}, msg: "the tag is too long: at least 7 bytes (max 6)"},
		{name: "a tag after a long tag", in: "<a" + long + "><b>", maxTag: 103},
		{name: "a start of a comment that is not one", in: "<!-x" + long + "<a>", maxTag: 5},
		{name: "a start of a CDATA section that is not one", in: "<![CDAT" + long + "<a>", maxTag: 5},
		{name: "a tag that is not closed", in: "<a b=\"" + long, maxTag: 20, at: Pos{1, 1}, msg: "the tag is too long: at least 21 bytes (max 20)"},
		{name: "in a long line the place is counted by characters", in: "あい<あ" + long, maxTag: 20, at: Pos{1, 3}, msg: "the tag is too long: at least 21 bytes (max 20)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := readAll(testInput(tt.in, 0, tt.maxTag))
			var e *Error
			switch {
			case tt.msg == "":
				if err != nil {
					t.Errorf("err = %v, want none", err)
				}
			case !errors.As(err, &e) || e.Pos != tt.at || e.Msg != tt.msg:
				t.Errorf("err = %v, want %d:%d: %s", err, tt.at.Line, tt.at.Column, tt.msg)
			}
		})
	}
}

func TestInputReaderErrors(t *testing.T) {
	boom := errors.New("boom")
	t.Run("after some bytes", func(t *testing.T) {
		in := newInput(io.MultiReader(strings.NewReader("abcd"), iotest.ErrReader(boom)), Options{MaxBytes: MaxBytes, MaxTagBytes: MaxTagBytes})
		data, _, err := readAll(in)
		var re *readError
		if string(data) != "abcd" || !errors.Is(err, boom) || !errors.As(err, &re) {
			t.Fatalf("data = %q, err = %v, want abcd and a readError of boom", data, err)
		}
		var e *Error
		if errors.As(err, &e) {
			t.Errorf("err = %v is an *Error, which has a place in the file", err)
		}
		if _, err2 := in.ReadByte(); !errors.Is(err2, err) {
			t.Errorf("ReadByte() = %v, want the same error again", err2)
		}
	})
	t.Run("at the start", func(t *testing.T) {
		_, _, err := readAll(newInput(iotest.ErrReader(boom), Options{MaxBytes: MaxBytes, MaxTagBytes: MaxTagBytes}))
		var re *readError
		if !errors.Is(err, boom) || !errors.As(err, &re) {
			t.Errorf("err = %v, want a readError of boom", err)
		}
	})
	t.Run("in the first three bytes", func(t *testing.T) {
		in := newInput(io.MultiReader(strings.NewReader("a"), iotest.ErrReader(boom)), Options{MaxBytes: MaxBytes, MaxTagBytes: MaxTagBytes})
		_, _, err := readAll(in)
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want boom", err)
		}
	})
	t.Run("the end is io.EOF itself", func(t *testing.T) {
		in := testInput("a", 0, 0)
		mustReadByte(t, in)
		for i := 0; i < 3; i++ {
			if _, err := in.ReadByte(); !isEOF(err) {
				t.Errorf("ReadByte() = %v, want io.EOF", err)
			}
		}
	})
	t.Run("a reader that gives a byte at a time", func(t *testing.T) {
		s := "<a>あい\x08う</a>"
		in := newInput(iotest.OneByteReader(strings.NewReader(s)), Options{MaxBytes: MaxBytes, MaxTagBytes: MaxTagBytes})
		data, _, err := readAll(in)
		if err != nil || string(data) != "<a>あいう</a>" {
			t.Errorf("data = %q, %v", data, err)
		}
	})
	t.Run("a reader that gives the end with the last bytes", func(t *testing.T) {
		in := newInput(iotest.DataErrReader(strings.NewReader("<a>あ</a>")), Options{MaxBytes: MaxBytes, MaxTagBytes: MaxTagBytes})
		data, _, err := readAll(in)
		if err != nil || string(data) != "<a>あ</a>" {
			t.Errorf("data = %q, %v", data, err)
		}
	})
}

func TestInputRead(t *testing.T) {
	const s = "\xef\xbb\xbfa\x08bc\nあ"
	got, err := io.ReadAll(testInput(s, 0, 0))
	if err != nil || string(got) != "abc\nあ" {
		t.Errorf("ReadAll() = %q, %v, want what ReadByte returns", got, err)
	}
	in := testInput(s, 0, 0)
	var parts []string
	buf := make([]byte, 2)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			parts = append(parts, string(buf[:n]))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
	}
	if want := []string{"ab", "c\n", "\xe3\x81", "\x82"}; !reflect.DeepEqual(parts, want) {
		t.Errorf("Read() in parts of 2 bytes = %q, want %q", parts, want)
	}
	if n, err := testInput("", 0, 0).Read(nil); n != 0 || err != nil {
		t.Errorf("Read(nil) = %d, %v, want 0, nil", n, err)
	}
	// What the decoder asks for when it opens a different encoding is the input, which must be a reader.
	var _ io.Reader = testInput("", 0, 0)
	var _ io.ByteReader = testInput("", 0, 0)
}

// TestInputTextStart checks textPos, which is where the text that is not blank in the last token begins, at the
// tokens of the decoder that have such text.
func TestInputTextStart(t *testing.T) {
	tests := []struct {
		in   string
		want []Pos
	}{
		{"<a>  x</a>", []Pos{{1, 6}}},
		{"<a>\n\n  x</a>", []Pos{{3, 3}}},
		{"<a><![CDATA[ \n x]]></a>", []Pos{{2, 2}}},
		{"<a>b<![CDATA[x]]>c</a>", []Pos{{1, 4}, {1, 14}, {1, 18}}},
		{"<a><!-- c -->  x</a>", []Pos{{1, 16}}},
		{"<a><?p?>\tx</a>", []Pos{{1, 10}}},
		{"<a>&amp;</a>", []Pos{{1, 4}}},
		{"<a> <b>x</b> y</a>", []Pos{{1, 8}, {1, 14}}},
		{"<a>\n<![CDATA[ ]]>\n<b>z</b></a>", []Pos{{3, 4}}},
	}
	for _, tt := range tests {
		in := testInput(tt.in, 0, 0)
		d := xml.NewDecoder(in)
		var got []Pos
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if cd, ok := tok.(xml.CharData); ok && !blank(cd) {
				got = append(got, in.textPos)
			}
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: textPos of the texts = %v, want %v", tt.in, got, tt.want)
		}
	}
}
