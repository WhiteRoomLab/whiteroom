package content

import (
	"errors"
	"fmt"
	"strings"
)

// Error is a problem at Path, an RFC 6901 JSON Pointer ("" for the root). In a document read from a file, it is
// also at Line and Column of File, which start at 1; Column counts characters.
type Error struct {
	Path   string
	Msg    string
	File   string
	Line   int
	Column int
}

func (e *Error) Error() string {
	s := e.Msg
	if e.Path != "" {
		s = e.Path + ": " + s
	}
	switch {
	case e.Line > 0 && e.File != "":
		s = fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Column, s)
	case e.Line > 0:
		s = fmt.Sprintf("%d:%d: %s", e.Line, e.Column, s)
	case e.File != "":
		s = e.File + ": " + s
	}
	return s
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

// required checks v, the value of the required item key at the root, with check, or reports it missing.
func (c *checker) required(key, v string, check func(string) error) {
	if v == "" {
		c.addf("/"+key, "missing %s", key)
	} else {
		c.add("/"+key, check(v))
	}
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
