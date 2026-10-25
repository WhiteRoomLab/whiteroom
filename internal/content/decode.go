package content

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strconv"
)

type decoder struct {
	dec  *jsontext.Decoder
	base string // path of the decoded value from the document root
}

// unmarshal reads exactly one value from data with read.
func unmarshal(data []byte, base string, read func(*decoder) error) error {
	// Not bytes.NewReader: jsontext then garbles member names in the pointers of errors inside ReadValue.
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(data)), base: base}
	if err := read(d); err != nil {
		return err
	}
	// The decoder accepts a stream of values, so reject anything after the first.
	if _, err := d.dec.ReadToken(); !errors.Is(err, io.EOF) {
		return &Error{Path: base, Msg: "unexpected data after value"}
	}
	return nil
}

// decodeJSON is the body of UnmarshalJSON: *x changes only if all of data is read without error.
func decodeJSON[T any, P interface {
	*T
	decode(*decoder) error
}](x P, data []byte) error {
	var v T
	if err := unmarshal(data, "", P(&v).decode); err != nil {
		return err
	}
	*x = v
	return nil
}

func (d *decoder) pointer() string {
	return d.base + string(d.dec.StackPointer())
}

// errorf returns an *Error at the most recently read token.
func (d *decoder) errorf(format string, args ...any) error {
	return &Error{Path: d.pointer(), Msg: fmt.Sprintf(format, args...)}
}

// fail converts an error from d.dec into an *Error.
func (d *decoder) fail(err error) error {
	// SyntacticError first: its JSONPointer is more precise than StackPointer, e.g. for truncated input.
	var se *jsontext.SyntacticError
	if errors.As(err, &se) {
		msg := se.Err.Error()
		switch {
		case errors.Is(se.Err, io.ErrUnexpectedEOF):
			msg = "unexpected end of JSON input"
		case errors.Is(se.Err, jsontext.ErrDuplicateName):
			msg = "duplicate key"
		}
		return &Error{Path: d.base + string(se.JSONPointer), Msg: msg}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &Error{Path: d.pointer(), Msg: "unexpected end of JSON input"}
	}
	return &Error{Path: d.pointer(), Msg: err.Error()}
}

// next reads a token of the type want ("object", "string", ...).
func (d *decoder) next(want string) (jsontext.Token, error) {
	tok, err := d.dec.ReadToken()
	if err != nil {
		return jsontext.Token{}, d.fail(err)
	}
	if got := kindName(tok.Kind()); got != want {
		return jsontext.Token{}, d.errorf("expected %s, got %s", want, got)
	}
	return tok, nil
}

func kindName(k jsontext.Kind) string {
	switch k {
	case jsontext.KindNull:
		return "null"
	case jsontext.KindFalse, jsontext.KindTrue:
		return "boolean"
	case jsontext.KindString:
		return "string"
	case jsontext.KindNumber:
		return "number"
	case jsontext.KindBeginObject:
		return "object"
	case jsontext.KindBeginArray:
		return "array"
	case jsontext.KindInvalid, jsontext.KindEndObject, jsontext.KindEndArray:
	}
	return k.String()
}

// object reads a non-empty object. member is called with each key and must read its value.
func (d *decoder) object(member func(key string) error) error {
	if _, err := d.next("object"); err != nil {
		return err
	}
	if d.dec.PeekKind() == jsontext.KindEndObject {
		return d.errorf("empty object is not allowed")
	}
	for d.dec.PeekKind() != jsontext.KindEndObject {
		name, err := d.dec.ReadToken()
		if err != nil {
			return d.fail(err)
		}
		if err := member(name.String()); err != nil {
			return err
		}
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return d.fail(err)
	}
	return nil
}

// items reads an array, which may be empty. elem must read the element.
func (d *decoder) items(elem func(i int) error) error {
	if _, err := d.next("array"); err != nil {
		return err
	}
	for i := 0; d.dec.PeekKind() != jsontext.KindEndArray; i++ {
		if err := elem(i); err != nil {
			return err
		}
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return d.fail(err)
	}
	return nil
}

// array reads a non-empty array.
func (d *decoder) array(elem func(i int) error) error {
	n := 0
	err := d.items(func(i int) error {
		n++
		return elem(i)
	})
	if err == nil && n == 0 {
		return d.errorf("empty array is not allowed")
	}
	return err
}

// string reads a non-empty string.
func (d *decoder) string() (string, error) {
	tok, err := d.next("string")
	if err != nil {
		return "", err
	}
	s := tok.String()
	if s == "" {
		return "", d.errorf("empty string is not allowed")
	}
	return s, nil
}

func (d *decoder) bool() (bool, error) {
	tok, err := d.next("boolean")
	if err != nil {
		return false, err
	}
	return tok.Bool(), nil
}

// number returns the number as written, such as "1.50".
func (d *decoder) number() (string, error) {
	tok, err := d.next("number")
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// int reads a number written as an integer: 2, but not 2.0 or 2e0.
func (d *decoder) int() (int, error) {
	s, err := d.number()
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, d.errorf("must be an integer")
	}
	return v, nil
}

// raw reads the next value as written and returns it with its path.
func (d *decoder) raw() (jsontext.Value, string, error) {
	v, err := d.dec.ReadValue()
	if err != nil {
		return nil, "", d.fail(err)
	}
	// v is only valid until the next read.
	return v.Clone(), d.pointer(), nil
}
