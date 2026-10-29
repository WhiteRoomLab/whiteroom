// Package yaml reads a subset of YAML 1.2 into JSON, for the files that developers write, such as site/schema.yaml.
// Anything outside the subset is an error.
package yaml

//go:generate go run ./gen

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Position is a place in the input. Line and Column start at 1, and Column counts characters (code points).
type Position struct{ Line, Column int }

// Error is a syntax error, or a feature outside the subset, at Position.
type Error struct {
	Position
	Msg string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Msg)
}

// ToJSON reads data as one YAML document and returns it as compact JSON, with the keys of mappings in the written
// order and the types of the YAML 1.2 core schema. positions maps the JSON Pointer of every value to the first
// character of its key for a member, of the value for an item, and of the document for "". Errors are *Error.
func ToJSON(data []byte) (out []byte, positions map[string]Position, err error) {
	lines, err := splitLines(data)
	if err != nil {
		return nil, nil, err
	}
	p := &parser{lines: lines, pos: make(map[string]Position), plainEnd: -1}
	if err := p.document(); err != nil {
		return nil, nil, err
	}
	return p.out, p.pos, nil
}

// splitLines checks the characters of data and returns its lines without line breaks and the byte order mark.
func splitLines(data []byte) ([]string, error) {
	s := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	line, col := 1, 1
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		pos := Position{Line: line, Column: col}
		switch {
		case r == utf8.RuneError && n == 1:
			return nil, &Error{Position: pos, Msg: "invalid UTF-8"}
		case r == '\n':
			line, col = line+1, 0
		case r == '\r':
			if i+1 == len(s) || s[i+1] != '\n' {
				return nil, &Error{Position: pos, Msg: "lone CR is not allowed; use LF or CRLF line breaks"}
			}
		case r == byteOrderMark:
			return nil, &Error{Position: pos, Msg: "byte order mark U+FEFF is allowed only at the start of the file"}
		case !printable(r):
			return nil, &Error{Position: pos, Msg: fmt.Sprintf("character U+%04X is not allowed", r)}
		}
		i += n
		col++
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines, nil
}
