package content

import (
	"errors"
	"fmt"
	"strings"
)

// Error is a problem at Path, an RFC 6901 JSON Pointer ("" for the root).
type Error struct {
	Path string
	Msg  string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

// Errors lists the problems found by Validate, in document order.
type Errors []*Error

func (es Errors) Error() string {
	msgs := make([]string, len(es))
	for i, e := range es {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "\n")
}

type checker struct {
	errs Errors
	doc  *docState // nil outside a document of blocks, as in Style.Validate and RichText.Validate
}

func (c *checker) addf(path, format string, args ...any) {
	c.errs = append(c.errs, &Error{Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (c *checker) add(path string, err error) {
	if err == nil {
		return
	}
	c.errs = append(c.errs, &Error{Path: path, Msg: err.Error()})
}

// addError adds err, which has its own path as an *Error from reading JSON, if it is not nil.
func (c *checker) addError(err error) {
	var e *Error
	switch {
	case err == nil:
	case errors.As(err, &e):
		c.errs = append(c.errs, e)
	default:
		c.errs = append(c.errs, &Error{Msg: err.Error()})
	}
}

func (c *checker) result() error {
	if len(c.errs) == 0 {
		return nil
	}
	return c.errs
}
