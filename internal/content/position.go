package content

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// jsonPositions returns the position of every value in data, a JSON document, by JSON Pointer, as yaml.ToJSON does:
// the first character of the key for a member, and of the value for an item and for "". A syntax error is an *Error
// at its position, without a Path.
func jsonPositions(data []byte) (map[string]yaml.Position, error) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(data))
	c := &cursor{data: data, pos: yaml.Position{Line: 1, Column: 1}}
	positions := make(map[string]yaml.Position)
	for {
		prev := int(dec.InputOffset())
		parent, n := dec.StackIndex(dec.StackDepth())
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, c.syntaxError(err)
		}
		switch k := tok.Kind(); {
		case k == jsontext.KindEndObject || k == jsontext.KindEndArray:
		case parent == jsontext.KindBeginObject && n%2 == 1:
			// The value of a member is at its key.
		default:
			positions[string(dec.StackPointer())] = c.at(skip(data, prev, ", \t\n\r"))
		}
		if dec.StackDepth() == 0 {
			break
		}
	}
	// The decoder accepts a stream of values, so reject anything after the first.
	if end := skip(data, int(dec.InputOffset()), " \t\n\r"); end < len(data) {
		return nil, c.errorAt(end, "unexpected data after value")
	}
	return positions, nil
}

// skip returns the offset of the first byte at or after off in data that is not in cutset.
func skip(data []byte, off int, cutset string) int {
	return len(data) - len(bytes.TrimLeft(data[off:], cutset))
}

// cursor finds the positions of offsets in data, which must not decrease.
type cursor struct {
	data []byte
	off  int
	pos  yaml.Position
}

func (c *cursor) at(off int) yaml.Position {
	for _, b := range c.data[c.off:off] {
		switch {
		case b == '\n':
			c.pos = yaml.Position{Line: c.pos.Line + 1, Column: 1}
		case utf8.RuneStart(b):
			c.pos.Column++
		}
	}
	c.off = off
	return c.pos
}

func (c *cursor) errorAt(off int, msg string) *Error {
	pos := c.at(off)
	return &Error{Msg: msg, Line: pos.Line, Column: pos.Column}
}

// syntaxError returns the *Error for err from ReadToken.
func (c *cursor) syntaxError(err error) *Error {
	// Without a value, ReadToken returns io.EOF, which is not a SyntacticError.
	off := len(c.data)
	var se *jsontext.SyntacticError
	if errors.As(err, &se) {
		off = int(se.ByteOffset)
	}
	return c.errorAt(off, syntaxMsg(err))
}

// locate returns copies of the errors in err, which are about the document in file, with File set to file. Those
// without a Line get the position of their Path or, if positions does not have it, of its nearest ancestor, such as
// the parent of a missing key. An error that is neither an *Error nor Errors becomes an *Error at the root.
func locate(err error, file string, positions map[string]yaml.Position) Errors {
	var es Errors
	var e *Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &es):
	case errors.As(err, &e):
		es = Errors{e}
	default:
		es = Errors{{Msg: err.Error()}}
	}
	located := make(Errors, len(es))
	for i, e := range es {
		c := *e
		c.File = file
		if c.Line == 0 {
			if pos, ok := nearest(positions, c.Path); ok {
				c.Line, c.Column = pos.Line, pos.Column
			}
		}
		located[i] = &c
	}
	return located
}

// nearest returns the position of the value at ptr or, if positions does not have it, of its nearest ancestor.
func nearest(positions map[string]yaml.Position, ptr string) (yaml.Position, bool) {
	for {
		if pos, ok := positions[ptr]; ok {
			return pos, true
		}
		i := strings.LastIndexByte(ptr, '/')
		if i < 0 {
			return yaml.Position{}, false
		}
		ptr = ptr[:i]
	}
}
