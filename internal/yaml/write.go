package yaml

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// FromJSON writes data, one JSON object, as YAML that ToJSON reads back as data without whitespace, with the strings
// escaped as jsontext.AppendQuote does. Lists of scalars are in the flow style, and other collections in the block style.
// null, nesting deeper than ToJSON reads, a key longer than ToJSON reads, and characters YAML cannot have are errors.
func FromJSON(data []byte) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(data))
	if dec.PeekKind() != jsontext.KindBeginObject {
		// Report a syntax error as such, and any other JSON as not an object.
		if _, err := dec.ReadToken(); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("FromJSON: %w", err)
		}
		return nil, errors.New("FromJSON: the JSON must be an object")
	}
	root, err := readJSON(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected data after the object")
		}
		return nil, fmt.Errorf("FromJSON: %w", err)
	}
	var w writer
	if len(root.names) == 0 {
		w.b = append(w.b, "{}\n"...)
	} else if err := w.object(root, 0, false); err != nil {
		return nil, err
	}
	// Do not write what ToJSON does not read as data.
	if got, _, err := ToJSON(w.b); err != nil || !bytes.Equal(got, root.appendJSON(nil)) {
		return nil, errors.New("FromJSON: the YAML does not read back to the same JSON")
	}
	return w.b, nil
}

type jsonKind int

const (
	jsonLiteral jsonKind = iota // a number, true or false
	jsonString
	jsonObject
	jsonArray
)

type jsonValue struct {
	kind   jsonKind
	text   string       // a string, or a number, true or false as written
	names  []string     // the names of an object
	values []*jsonValue // the values of an object, or the elements of an array
}

// readJSON reads the next value of dec, which is in depth collections.
func readJSON(dec *jsontext.Decoder, depth int) (*jsonValue, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, fmt.Errorf("FromJSON: %w", err)
	}
	var v *jsonValue
	var end jsontext.Kind
	switch tok.Kind() {
	case jsontext.KindNull:
		return nil, errors.New("FromJSON: null is not in the YAML that ToJSON reads")
	case jsontext.KindFalse, jsontext.KindTrue, jsontext.KindNumber:
		return &jsonValue{kind: jsonLiteral, text: tok.String()}, nil
	case jsontext.KindString:
		return &jsonValue{kind: jsonString, text: tok.String()}, nil
	case jsontext.KindBeginObject:
		v, end = &jsonValue{kind: jsonObject}, jsontext.KindEndObject
	case jsontext.KindBeginArray:
		v, end = &jsonValue{kind: jsonArray}, jsontext.KindEndArray
	case jsontext.KindInvalid, jsontext.KindEndObject, jsontext.KindEndArray:
		// The decoder reports these as errors.
		return nil, errors.New("FromJSON: unexpected JSON token")
	}
	if depth++; depth > maxDepth {
		return nil, fmt.Errorf("FromJSON: nested too deeply (max %d)", maxDepth)
	}
	for dec.PeekKind() != end {
		if v.kind == jsonObject {
			name, err := dec.ReadToken()
			if err != nil {
				return nil, fmt.Errorf("FromJSON: %w", err)
			}
			v.names = append(v.names, name.String())
		}
		e, err := readJSON(dec, depth)
		if err != nil {
			return nil, err
		}
		v.values = append(v.values, e)
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, fmt.Errorf("FromJSON: %w", err)
	}
	return v, nil
}

// appendJSON appends v as ToJSON writes it.
func (v *jsonValue) appendJSON(dst []byte) []byte {
	switch v.kind {
	case jsonObject:
		dst = append(dst, '{')
		for i, name := range v.names {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst, _ = jsontext.AppendQuote(dst, name)
			dst = append(dst, ':')
			dst = v.values[i].appendJSON(dst)
		}
		return append(dst, '}')
	case jsonArray:
		dst = append(dst, '[')
		for i, e := range v.values {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = e.appendJSON(dst)
		}
		return append(dst, ']')
	case jsonString:
		dst, _ = jsontext.AppendQuote(dst, v.text)
		return dst
	case jsonLiteral:
	}
	return append(dst, v.text...)
}

// block reports whether v is written in the block style: an object with members, or an array with a collection.
func (v *jsonValue) block() bool {
	switch v.kind {
	case jsonObject:
		return len(v.names) > 0
	case jsonArray:
		return slices.ContainsFunc(v.values, func(e *jsonValue) bool { return e.kind == jsonObject || e.kind == jsonArray })
	case jsonLiteral, jsonString:
	}
	return false
}

type writer struct{ b []byte }

func (w *writer) spaces(n int) {
	for range n {
		w.b = append(w.b, ' ')
	}
}

// object writes the members of v, which has some, as a block mapping with the indentation indent. The first member
// follows "- " if first.
func (w *writer) object(v *jsonValue, indent int, first bool) error {
	for i, name := range v.names {
		if !first || i > 0 {
			w.spaces(indent)
		}
		key, err := scalarText(name, inKey)
		if err != nil {
			return err
		}
		if utf8.RuneCountInString(key) > maxKeyLen {
			return fmt.Errorf("FromJSON: the key %.40q is longer than %d characters", name, maxKeyLen)
		}
		w.b = append(w.b, key...)
		w.b = append(w.b, ':')
		if err := w.value(v.values[i], indent+2, true); err != nil {
			return err
		}
	}
	return nil
}

// sequence writes the elements of v, which has some, as a block sequence with the indentation indent. The first
// element follows "- " if first.
func (w *writer) sequence(v *jsonValue, indent int, first bool) error {
	for i, e := range v.values {
		if !first || i > 0 {
			w.spaces(indent)
		}
		w.b = append(w.b, "- "...)
		if err := w.value(e, indent+2, false); err != nil {
			return err
		}
	}
	return nil
}

// value writes v and the line break, after the key of a member or after "- ". A collection in the block style has the
// indentation indent, and starts on the line after the key, or on the line of "- ".
func (w *writer) value(v *jsonValue, indent int, member bool) error {
	if v.block() {
		if member {
			w.b = append(w.b, '\n')
		}
		if v.kind == jsonObject {
			return w.object(v, indent, !member)
		}
		return w.sequence(v, indent, !member)
	}
	s, err := flowText(v)
	if err != nil {
		return err
	}
	if member {
		w.b = append(w.b, ' ')
	}
	w.b = append(w.b, s...)
	w.b = append(w.b, '\n')
	return nil
}

// flowText returns v, which is not in the block style, in one line.
func flowText(v *jsonValue) (string, error) {
	switch v.kind {
	case jsonObject:
		return "{}", nil
	case jsonArray:
		var b strings.Builder
		b.WriteByte('[')
		for i, e := range v.values {
			if i > 0 {
				b.WriteString(", ")
			}
			s := e.text
			if e.kind == jsonString {
				var err error
				if s, err = scalarText(s, inElement); err != nil {
					return "", err
				}
			}
			b.WriteString(s)
		}
		b.WriteByte(']')
		return b.String(), nil
	case jsonString:
		return scalarText(v.text, inValue)
	case jsonLiteral:
	}
	return v.text, nil
}

// place is where a string is written.
type place int

const (
	inValue   place = iota // after "key: " or "- "
	inElement              // in a flow sequence
	inKey
)

// scalarText returns s as a scalar at p: without quotes if ToJSON reads that as s, and in double quotes otherwise.
func scalarText(s string, p place) (string, error) {
	for _, r := range s {
		// ToJSON allows C0 controls only escaped.
		if r >= 0x20 && (!printable(r) || r == byteOrderMark) {
			return "", fmt.Errorf("FromJSON: the string %q has the character U+%04X, which YAML cannot have", s, r)
		}
	}
	if s != "" && !strings.ContainsFunc(s, needsEscape) && readsAsPlain(s, p) {
		return s, nil
	}
	return quote(s), nil
}

// needsEscape reports whether r is always written as an escape, so a string with it is quoted.
func needsEscape(r rune) bool {
	return r < 0x20 || r == 0x85 || r == 0x2028 || r == 0x2029
}

// readsAsPlain reports whether ToJSON reads s without quotes at p as s. It asks ToJSON about one line, so that the
// rules of ToJSON are not written twice.
func readsAsPlain(s string, p place) bool {
	quoted, _ := jsontext.AppendQuote(nil, s)
	var line, want string
	switch p {
	case inValue:
		line, want = "k: "+s+"\n", `{"k":`+string(quoted)+"}"
	case inElement:
		line, want = "k: ["+s+"]\n", `{"k":[`+string(quoted)+"]}"
	case inKey:
		line, want = s+": 0\n", "{"+string(quoted)+":0}"
	}
	got, _, err := ToJSON([]byte(line))
	return err == nil && string(got) == want
}

// quote returns s in double quotes, with the escapes that ToJSON reads.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0x85:
			b.WriteString(`\N`)
		case 0x2028:
			b.WriteString(`\L`)
		case 0x2029:
			b.WriteString(`\P`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\x%02X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
