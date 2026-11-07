package wxr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// maxListed is the number of removed characters that a note lists.
const maxListed = 10

type removedChar struct {
	pos Pos
	r   rune
}

// readError is an error of the reader under the input, which has no place in the file.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

type scanState uint8

const (
	inText      scanState = iota
	afterLT               // after <
	inTag                 // in a start tag or an end tag
	inPI                  // in <? ... ?>
	afterBang             // after <!
	afterDash             // after <!-
	inComment             // in <!-- ... -->
	inCDATAOpen           // in <![CDATA[
	inCDATA               // in <![CDATA[ ... ]]>
	inDecl                // in another <! ... >
)

// input is what encoding/xml reads. It passes on the bytes of the file, except for a byte order mark and the
// characters that XML does not allow, and it finds the problems that the decoder would report in the wrong place or
// not at all: invalid UTF-8, the size of the file and the length of a tag. It tracks the place in the file of every
// byte that it passes on.
type input struct {
	br       *bufio.Reader
	maxBytes int64
	maxTag   int

	started bool
	err     error // the first error, which every later read returns

	consumed  int64 // the bytes read from br
	line, col int   // of the next character to read from br

	// The character that is being passed on, byte by byte.
	pend         [utf8.UTFMax]byte
	pendN, pendI int
	charPos      Pos // of that character

	given   int64 // the bytes passed on
	lastPos Pos   // of the last byte passed on

	removed  [maxListed]removedChar
	nRemoved int // since the last drain, which can be more than maxListed

	// The scan of the tags in the bytes passed on.
	state  scanState
	n      int // the characters of CDATA[ matched, or the dashes or brackets before the current byte, or 1 after a ?
	quote  byte
	tagLen int
	tagPos Pos

	// The first byte that is not white space in the text, or CDATA section, that is being passed on.
	textPos Pos
	textSet bool
}

func newInput(r io.Reader, o Options) *input {
	return &input{br: bufio.NewReaderSize(r, 64<<10), maxBytes: o.MaxBytes, maxTag: o.MaxTagBytes, line: 1, col: 1}
}

func (in *input) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		b, err := in.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		p[n] = b
		n++
	}
	return n, nil
}

func (in *input) ReadByte() (byte, error) {
	if in.err != nil {
		return 0, in.err
	}
	if in.pendI == in.pendN {
		if err := in.fill(); err != nil {
			in.err = err
			return 0, err
		}
	}
	b := in.pend[in.pendI]
	in.pendI++
	in.given++
	in.lastPos = in.charPos
	if err := in.scan(b); err != nil {
		in.err = err
		return 0, err
	}
	return b, nil
}

// start checks the first bytes of the file for a byte order mark.
func (in *input) start() error {
	p, err := in.br.Peek(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return &readError{err}
	}
	switch {
	case len(p) >= 2 && (p[0] == 0xFE && p[1] == 0xFF || p[0] == 0xFF && p[1] == 0xFE):
		return &Error{Pos{1, 1}, "UTF-16 is not supported; convert the WXR to UTF-8"}
	case len(p) == 3 && p[0] == 0xEF && p[1] == 0xBB && p[2] == 0xBF:
		_, _ = in.br.Discard(3)
		in.consumed += 3
	}
	return nil
}

// fill reads the next character that the decoder is to get into pend, and removes the characters before it that XML
// does not allow. It returns io.EOF at the end of the file.
func (in *input) fill() error {
	if !in.started {
		in.started = true
		if err := in.start(); err != nil {
			return err
		}
	}
	for {
		r, n, err := in.br.ReadRune()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return &readError{err}
		}
		pos := Pos{in.line, in.col}
		in.consumed += int64(n)
		switch {
		case in.consumed > in.maxBytes:
			return &Error{pos, fmt.Sprintf("too large: at least %d bytes (max %d)", in.maxBytes+1, in.maxBytes)}
		case r == utf8.RuneError && n == 1:
			return &Error{pos, "invalid UTF-8; the WXR must be in UTF-8"}
		}
		if r == '\n' {
			in.line++
			in.col = 1
		} else {
			in.col++
		}
		if !allowed(r) {
			if in.nRemoved < maxListed {
				in.removed[in.nRemoved] = removedChar{pos, r}
			}
			in.nRemoved++
			continue
		}
		if r < utf8.RuneSelf {
			in.pend[0], in.pendN = byte(r), 1
		} else {
			in.pendN = utf8.EncodeRune(in.pend[:], r)
		}
		in.pendI = 0
		in.charPos = pos
		return nil
	}
}

// allowed reports whether XML allows r, apart from the surrogates, which are not valid UTF-8.
func allowed(r rune) bool {
	return r >= 0x20 && r != 0xFFFE && r != 0xFFFF || r == '\t' || r == '\n' || r == '\r'
}

// pos returns the place in the file of the byte at off, an InputOffset of the decoder, which is the next byte that it
// reads: the one after the bytes passed on, or the last of them if the decoder has put it back.
func (in *input) pos(off int64) Pos {
	if off == in.given-1 {
		return in.lastPos
	}
	// Characters that are removed may come first, so read up to the next byte.
	if in.pendI == in.pendN && in.err == nil {
		if err := in.fill(); err != nil {
			in.err = err
		}
	}
	if in.pendI < in.pendN {
		return in.charPos
	}
	return Pos{in.line, in.col}
}

// drain returns the characters removed since the last call, at most maxListed of them, which are valid until the
// next read, and their number.
func (in *input) drain() ([]removedChar, int) {
	n := in.nRemoved
	in.nRemoved = 0
	return in.removed[:min(n, maxListed)], n
}

// scan follows the tags in the bytes passed on, and returns an error if one is too long. It does not limit the
// comments, the CDATA sections and the processing instructions, which hold the large values and the metadata.
func (in *input) scan(b byte) error {
	switch in.state {
	case inText:
		switch {
		case b == '<':
			in.state, in.tagLen, in.tagPos = afterLT, 1, in.charPos
		case !in.textSet && !isSpace(b):
			in.textPos, in.textSet = in.charPos, true
		}
	case afterLT:
		if err := in.count(); err != nil {
			return err
		}
		switch b {
		case '?':
			in.state, in.n = inPI, 0
		case '!':
			in.state = afterBang
		default:
			in.state, in.quote = inTag, 0
			in.follow(b)
		}
	case afterBang:
		if err := in.count(); err != nil {
			return err
		}
		switch b {
		case '-':
			in.state = afterDash
		case '[':
			in.state, in.n = inCDATAOpen, 0
		default:
			// The byte after <! belongs to the declaration, even if it is a >.
			in.state, in.quote = inDecl, 0
		}
	case inTag, inDecl:
		if err := in.count(); err != nil {
			return err
		}
		in.follow(b)
	case inPI:
		switch {
		case b == '>' && in.n == 1:
			in.toText()
		case b == '?':
			in.n = 1
		default:
			in.n = 0
		}
	case afterDash:
		if b == '-' {
			in.state, in.n = inComment, 0
		} else {
			in.toText()
		}
	case inComment:
		switch {
		case b == '>' && in.n >= 2:
			in.toText()
		case b == '-':
			in.n = min(in.n+1, 2)
		default:
			in.n = 0
		}
	case inCDATAOpen:
		switch {
		case b != "CDATA["[in.n]:
			in.toText()
		case in.n == len("CDATA[")-1:
			in.state, in.n, in.textSet = inCDATA, 0, false
		default:
			in.n++
		}
	case inCDATA:
		if !in.textSet && !isSpace(b) {
			in.textPos, in.textSet = in.charPos, true
		}
		switch {
		case b == '>' && in.n >= 2:
			in.toText()
		case b == ']':
			in.n = min(in.n+1, 2)
		default:
			in.n = 0
		}
	}
	return nil
}

func (in *input) toText() {
	in.state, in.textSet = inText, false
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

// count counts a byte of a tag or a declaration.
func (in *input) count() error {
	if in.tagLen++; in.tagLen > in.maxTag {
		return &Error{in.tagPos, fmt.Sprintf("the tag is too long: at least %d bytes (max %d)", in.maxTag+1, in.maxTag)}
	}
	return nil
}

// follow follows b in a tag or a declaration, which a > in quotes does not end.
func (in *input) follow(b byte) {
	switch {
	case in.quote != 0:
		if b == in.quote {
			in.quote = 0
		}
	case b == '"' || b == '\'':
		in.quote = b
	case b == '>':
		in.toText()
	}
}
