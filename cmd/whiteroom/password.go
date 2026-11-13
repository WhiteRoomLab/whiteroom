package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// maxPasswordLine is the most bytes of a password that is read, which is also the most that internal/account takes.
const maxPasswordLine = 8 << 10

var (
	// errMismatch is the error of two entries of a password that differ, which the caller may ask for again.
	errMismatch = errors.New("the two passwords are not the same")

	errNoPassword = errors.New("no password was read from the standard input")
	errTooLong    = fmt.Errorf("the password is longer than %d bytes", maxPasswordLine)
	errNoHidden   = errors.New("cannot read a password without echo from a terminal on this OS: " +
		"give it on the standard input from a pipe or a file")
)

// passwordReader reads a password from in: from the terminal, twice and without echo, if in is one that can be read
// so, or else as the first line of in. Nothing that it returns or prints has what it has read.
type passwordReader struct {
	in      io.Reader
	prompts io.Writer
	tty     *os.File // in, if it is a terminal
}

func newPasswordReader(in io.Reader, prompts io.Writer) *passwordReader {
	r := &passwordReader{in: in, prompts: prompts}
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		r.tty = f
	}
	return r
}

// terminal reports whether read asks for the password on a terminal, which it may ask for again after errMismatch.
func (r *passwordReader) terminal() bool { return r.tty != nil }

// read reads a password. From a terminal, it asks with prompt and then again with "prompt (again)", and returns
// errMismatch if the two are not the same; the terminal is as it was when read returns, also when ctx is done first.
// From another input, it reads the first line, without its line break, and does not ask again; the line must not be
// empty.
func (r *passwordReader) read(ctx context.Context, prompt string) (string, error) {
	if r.tty == nil {
		line, err := readLine(r.in, maxPasswordLine, false)
		switch {
		case errors.Is(err, io.EOF) || err == nil && len(line) == 0:
			return "", errNoPassword
		case err != nil:
			return "", err
		}
		defer clear(line)
		return string(line), nil
	}
	if !canReadHidden {
		return "", errNoHidden
	}
	first, err := r.ask(ctx, prompt+": ")
	if err != nil {
		return "", err
	}
	second, err := r.ask(ctx, prompt+" (again): ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errMismatch
	}
	return first, nil
}

// ask prints prompt, and reads a line from the terminal without echo, and then ends the line of the prompt, which the
// terminal does not echo.
func (r *passwordReader) ask(ctx context.Context, prompt string) (string, error) {
	printf(r.prompts, "%s", prompt)
	line, err := readHidden(ctx, r.tty, maxPasswordLine)
	printf(r.prompts, "\n")
	if errors.Is(err, io.EOF) {
		return "", errNoPassword
	}
	if err != nil {
		return "", err
	}
	defer clear(line)
	return string(line), nil
}

// readLine reads a line from r, a byte at a time so that it takes nothing after the line, and returns it without its
// "\n" or "\r\n". The input may end the line instead; if it ends before any byte, the error is io.EOF. A line of more
// than limit bytes is errTooLong, as soon as it is known, or, if drain is true, after the rest of the line is read.
func readLine(r io.Reader, limit int, drain bool) ([]byte, error) {
	var line []byte
	var b [1]byte
	// tooLong fails; if the line has not ended yet, and drain is true, it reads the rest of the line first.
	tooLong := func(ended bool) ([]byte, error) {
		for drain && !ended {
			if n, err := r.Read(b[:]); err != nil || n == 1 && b[0] == '\n' {
				break
			}
		}
		clear(line)
		return nil, errTooLong
	}
	for {
		n, err := r.Read(b[:])
		if n == 1 {
			switch {
			case b[0] == '\n':
				line, _ = bytes.CutSuffix(line, []byte{'\r'})
				if len(line) > limit {
					return tooLong(true)
				}
				return line, nil
			// With a "\r" among them, the limit+1 bytes may still be a line of the limit.
			case len(line) > limit:
				return tooLong(false)
			}
			line = append(line, b[0])
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			if len(line) > limit {
				return tooLong(true)
			}
			return line, nil
		}
		if err != nil {
			clear(line)
			return nil, fmt.Errorf("cannot read the password: %w", err)
		}
	}
}
