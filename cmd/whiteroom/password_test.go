package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// TestReadLine checks the line that readLine reads: the end of the line, the limit with and without the "\r", that
// nothing after the line is read, and that the rest of a line that is too long is read only if asked for.
func TestReadLine(t *testing.T) {
	at := strings.Repeat("a", maxPasswordLine)
	for _, tt := range []struct {
		what  string
		input string
		want  string
		err   error
	}{
		{"a line", "abc\n", "abc", nil},
		{"a line of a carriage return and a line feed", "abc\r\n", "abc", nil},
		{"the end of the input", "abc", "abc", nil},
		{"a carriage return at the end of the input", "abc\r", "abc\r", nil},
		{"two carriage returns", "abc\r\r\n", "abc\r", nil},
		{"two lines", "abc\ndef\n", "abc", nil},
		{"a space", " a b \n", " a b ", nil},
		{"a line of no bytes", "\n", "", nil},
		{"no input", "", "", io.EOF},
		{"the limit", at + "\n", at, nil},
		{"the limit and a carriage return", at + "\r\n", at, nil},
		{"the limit and the end of the input", at, at, nil},
		{"one byte over the limit", at + "a\n", "", errTooLong},
		{"one byte over the limit and a carriage return", at + "a\r\n", "", errTooLong},
		{"one byte over the limit and the end of the input", at + "a", "", errTooLong},
		{"two carriage returns over the limit", at + "\r\r\n", "", errTooLong},
		{"far over the limit", at + at + "\n", "", errTooLong},
	} {
		for _, wrap := range []struct {
			name string
			wrap func(io.Reader) io.Reader
		}{
			{"as it is", func(r io.Reader) io.Reader { return r }},
			{"a byte at a time", iotest.OneByteReader},
			{"with the error of the end with the last byte", iotest.DataErrReader},
		} {
			for _, drain := range []bool{false, true} {
				in := strings.NewReader(tt.input)
				line, err := readLine(wrap.wrap(in), maxPasswordLine, drain)
				if string(line) != tt.want || !errors.Is(err, tt.err) {
					t.Errorf("%s, %s, drain %v: readLine = %q, %v; want %q, %v", tt.what, wrap.name, drain, line, err, tt.want, tt.err)
				}
			}
		}
	}

	// Nothing after the line is read, so that the standard input stays for what comes next.
	in := strings.NewReader("abc\r\ndef\n")
	if line, err := readLine(in, maxPasswordLine, false); string(line) != "abc" || err != nil {
		t.Fatalf("readLine = %q, %v; want abc", line, err)
	}
	if rest, _ := io.ReadAll(in); string(rest) != "def\n" {
		t.Errorf("after the first line, %q is left, want %q", rest, "def\n")
	}

	// A line that is too long is not read to its end, unless it is asked for.
	for _, drain := range []bool{false, true} {
		in := strings.NewReader(at + "xyz\nnext\n")
		if _, err := readLine(in, maxPasswordLine, drain); !errors.Is(err, errTooLong) {
			t.Fatalf("drain %v: readLine = %v, want %v", drain, err, errTooLong)
		}
		rest, _ := io.ReadAll(in)
		want := "z\nnext\n"
		if drain {
			want = "next\n"
		}
		if string(rest) != want {
			t.Errorf("drain %v: after the line that is too long, %q is left, want %q", drain, rest, want)
		}
	}

	// A line of the limit and the "\r\n" is whole, and a line that ends with the "\n" of a line that is too long is not.
	in = strings.NewReader(at + "a\n" + "next\n")
	if _, err := readLine(in, maxPasswordLine, true); !errors.Is(err, errTooLong) {
		t.Fatalf("readLine = %v, want %v", err, errTooLong)
	}
	if rest, _ := io.ReadAll(in); string(rest) != "next\n" {
		t.Errorf("after a line that is too long and has its line feed, %q is left, want %q", rest, "next\n")
	}

	failing := errors.New("the disk is gone")
	_, err := readLine(io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(failing)), maxPasswordLine, false)
	if !errors.Is(err, failing) || strings.Contains(err.Error(), "abc") {
		t.Errorf("readLine of a failing reader = %v, want an error that wraps %v and has nothing of the input", err, failing)
	}
}

// TestPasswordReader checks the password that a passwordReader reads from an input that is not a terminal: the first
// line, which it does not ask for again, and the errors, which have nothing of the input.
func TestPasswordReader(t *testing.T) {
	const secret = "the second Tuesday of a quiet month"
	for _, tt := range []struct {
		what  string
		input string
		want  string
		err   error
	}{
		{"a line", secret + "\n", secret, nil},
		{"a line with a carriage return", secret + "\r\n", secret, nil},
		{"the end of the input", secret, secret, nil},
		{"only the first line", secret + "\nthe next line\n", secret, nil},
		{"an empty line", "\n" + secret + "\n", "", errNoPassword},
		{"an empty line with a carriage return", "\r\n", "", errNoPassword},
		{"no input", "", "", errNoPassword},
		{"a line that is too long", strings.Repeat("s", maxPasswordLine+1) + "\n", "", errTooLong},
	} {
		var prompts strings.Builder
		in := strings.NewReader(tt.input)
		r := newPasswordReader(in, &prompts)
		if r.terminal() {
			t.Fatalf("%s: a reader of a strings.Reader is a terminal", tt.what)
		}
		got, err := r.read(t.Context(), "Password")
		if got != tt.want || !errors.Is(err, tt.err) || prompts.Len() > 0 {
			t.Errorf("%s: read = %q, %v, with the prompts %q; want %q, %v, and no prompt", tt.what, got, err, prompts.String(),
				tt.want, tt.err)
		}
		if err != nil && strings.Contains(err.Error(), "Tuesday") {
			t.Errorf("%s: the error has the input: %v", tt.what, err)
		}
	}

	r := newPasswordReader(errReader{}, io.Discard)
	if _, err := r.read(t.Context(), "Password"); !errors.Is(err, errBroken) {
		t.Errorf("read of a failing reader = %v, want an error that wraps %v", err, errBroken)
	}
}

var errBroken = errors.New("broken input")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBroken }
