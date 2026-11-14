package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
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

// maxPasswordAsks is the most times that a command asks for a password, the times that the two entries on a terminal are
// not the same and the times that the password is refused included.
const maxPasswordAsks = 3

// errProblemsTold is the error of askPassword for a password that try has refused with problems that asking again does not
// put right, which askPassword has told already.
var errProblemsTold = errors.New("the problems with the password are told")

// askPassword reads a password from the standard input of e and gives it to try, until try accepts it by returning nil.
// try refuses a password with content.Errors: they are printed with the name of command, and on a terminal, if every one
// of them is about the password, the password is asked for again; else the error is errProblemsTold. Any other error of
// try ends askPassword as it is. It asks at most maxPasswordAsks times.
func askPassword(ctx context.Context, e env, command, prompt string, try func(password string) error) error {
	reader := newPasswordReader(e.stdin, e.stderr)
	for asks := 0; ; {
		if asks == maxPasswordAsks {
			return fmt.Errorf("no password was accepted in %d tries", maxPasswordAsks)
		}
		asks++
		password, err := reader.read(ctx, prompt)
		if errors.Is(err, errMismatch) {
			printf(e.stderr, "%s: %v\n", command, err)
			continue
		}
		if err != nil {
			return err
		}
		var problems content.Errors
		if err := try(password); !errors.As(err, &problems) {
			return err
		}
		printInitProblems(e.stderr, command, problems)
		if !reader.terminal() || !onlyPasswordProblems(problems) {
			return errProblemsTold
		}
	}
}

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
// empty. If ctx is done first, read returns ctx.Err(), and the goroutine that reads stays until the input gives a line or
// ends.
func (r *passwordReader) read(ctx context.Context, prompt string) (string, error) {
	if r.tty == nil {
		line, err := readLineContext(ctx, r.in, maxPasswordLine, false)
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

// readLineContext is readLine, which returns ctx.Err() as soon as ctx is done, though r gives nothing. The goroutine that
// reads then stays until r gives a line or ends, and clears what it has read.
func readLineContext(ctx context.Context, r io.Reader, limit int, drain bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type result struct {
		line []byte
		err  error
	}
	read := make(chan result, 1)
	go func() {
		line, err := readLine(r, limit, drain)
		read <- result{line, err}
	}()
	select {
	case res := <-read:
		return res.line, res.err
	case <-ctx.Done():
		go func() { clear((<-read).line) }()
		return nil, ctx.Err()
	}
}

// readLine reads a line from r, a byte at a time so that it takes nothing after the line, and returns it without its
// "\n" or "\r\n". The input may end the line instead; if it ends before any byte, the error is io.EOF. A line of more
// than limit bytes is errTooLong, as soon as it is known, or, if drain is true, after the rest of the line is read.
func readLine(r io.Reader, limit int, drain bool) ([]byte, error) {
	// The most that the loop appends is limit+1 bytes, so that the buffer never moves: no copy of a prefix of the line is left.
	line := make([]byte, 0, limit+2)
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
